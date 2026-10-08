package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
)

// fleetStore é o que o estado da frota usa do banco: a avaliação e a lista de máquinas.
type fleetStore interface {
	complianceStore
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
}

// complianceService calcula o estado de compliance (RF-23) sob demanda, a cada pedido:
// a avaliação de uma máquina leva milissegundos, e a frota do laboratório é pequena. Com
// a frota real, o resultado passa a ser gravado (pendência de escala, RNF-07).
type complianceService struct {
	st     fleetStore
	logger *slog.Logger
	now    func() time.Time
}

func newComplianceService(st fleetStore, logger *slog.Logger) *complianceService {
	return &complianceService{st: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// Fleet avalia todas as máquinas, na ordem do que pede ação primeiro.
func (s *complianceService) Fleet(ctx context.Context) (protocol.ComplianceFleet, error) {
	list, err := s.st.Machines(ctx)
	if err != nil {
		return protocol.ComplianceFleet{}, err
	}
	now := s.now()
	fleet := protocol.ComplianceFleet{EvaluatedAt: now, Counts: map[string]int{}, Machines: make([]protocol.ComplianceStatus, 0, len(list))}
	for _, st := range compliance.States {
		fleet.Counts[string(st)] = 0
	}
	for _, m := range list {
		if err := ctx.Err(); err != nil {
			return protocol.ComplianceFleet{}, err
		}
		cs := s.status(ctx, m, now)
		fleet.Counts[cs.State]++
		fleet.Machines = append(fleet.Machines, cs)
	}
	sort.SliceStable(fleet.Machines, func(i, j int) bool {
		a, b := fleet.Machines[i], fleet.Machines[j]
		if ra, rb := compliance.State(a.State).Rank(), compliance.State(b.State).Rank(); ra != rb {
			return ra < rb
		}
		if a.Exploited != b.Exploited {
			return a.Exploited > b.Exploited
		}
		if a.Pending != b.Pending {
			return a.Pending > b.Pending
		}
		return a.MachineID < b.MachineID
	})
	return fleet, nil
}

// Machine avalia uma máquina pelo ID completo.
func (s *complianceService) Machine(ctx context.Context, id string) (protocol.ComplianceStatus, bool, error) {
	list, err := s.st.Machines(ctx)
	if err != nil {
		return protocol.ComplianceStatus{}, false, err
	}
	for _, m := range list {
		if m.ID == id {
			return s.status(ctx, m, s.now()), true, nil
		}
	}
	return protocol.ComplianceStatus{}, false, nil
}

// status avalia uma máquina. Um erro na avaliação de uma máquina não derruba a frota:
// ela fica "desconhecido" e o erro vai para o log.
func (s *complianceService) status(ctx context.Context, m protocol.MachineSummary, now time.Time) protocol.ComplianceStatus {
	cs := protocol.ComplianceStatus{
		MachineID: m.ID, Hostname: m.Hostname, OS: strings.TrimSpace(m.OSName + " " + m.OSVersion),
		LastSeenAt: m.LastSeenAt, EvaluatedAt: now,
	}
	f := compliance.Facts{LastSeen: m.LastSeenAt, RebootPending: m.RebootPending}
	ev, err := evaluateID(ctx, s.st, m.ID, evalOptions{Now: now})
	if err != nil {
		s.logger.Error("avaliação de compliance", "machine_id", m.ID, "error", err)
		f.Unevaluated = "erro na avaliação (veja o log do servidor)"
	} else {
		fillFacts(&f, &cs, ev)
	}
	state, reasons := compliance.Classify(f, now)
	cs.State = string(state)
	cs.Reasons = reasons
	if cs.Reasons == nil {
		cs.Reasons = []string{}
	}
	return cs
}

// fillFacts extrai da avaliação o que a classificação precisa e o que a resposta mostra.
func fillFacts(f *compliance.Facts, cs *protocol.ComplianceStatus, ev machineEval) {
	cs.Catalog, cs.CatalogWarnings = ev.Catalog, ev.Warnings
	if !ev.Collected.IsZero() {
		t := ev.Collected.UTC()
		cs.InventoryCollectedAt = &t
	}
	switch {
	case ev.NoInventory:
		f.NoInventory = true
		return
	case ev.Note != "":
		f.Unevaluated = ev.Note
		return
	}
	switch {
	case ev.Linux != nil:
		f.Pending, f.PendingUnit = len(ev.Linux.Findings), "pacotes"
		for _, x := range ev.Linux.Findings {
			if x.Exploited() || x.InferredExploited() {
				f.Exploited++
			}
		}
		f.NewerKernel = len(ev.Linux.Kernel.Newer) > 0
	case ev.Windows != nil:
		f.Pending, f.PendingUnit = len(ev.Windows.Pending), "CVEs"
		for _, c := range ev.Windows.Pending {
			if c.Explored() {
				f.Exploited++
			}
		}
	case ev.MacOS != nil:
		f.Pending, f.PendingUnit = len(ev.MacOS.Result.Pending), "CVEs"
		for _, c := range ev.MacOS.Result.Pending {
			if c.Exploited || c.KEV {
				f.Exploited++
			}
		}
		f.SupportEnded = ev.MacOS.Support.State == compliance.SupportEnded
	}
	cs.Pending, cs.PendingUnit, cs.Exploited = f.Pending, f.PendingUnit, f.Exploited
}

// printFleet escreve o estado da frota. Com summary, só as contagens (sem nomes: a saída
// pode ir para um chamado ou para o chat).
func printFleet(fleet protocol.ComplianceFleet, summary bool, out io.Writer) {
	parts := make([]string, 0, len(compliance.States))
	for _, st := range compliance.States {
		parts = append(parts, fmt.Sprintf("%s %d", st.Label(), fleet.Counts[string(st)]))
	}
	fmt.Fprintf(out, "Frota: %d máquina(s): %s\n", len(fleet.Machines), strings.Join(parts, " · "))
	if summary {
		return
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ESTADO\tMÁQUINA\tSO\tPENDENTES\tEXPLORADAS\tÚLTIMO CONTATO\tNOME\tMOTIVOS")
	for _, m := range fleet.Machines {
		id := m.MachineID
		if len(id) > 8 {
			id = id[:8]
		}
		pend := "-"
		if m.Pending > 0 {
			pend = fmt.Sprintf("%d %s", m.Pending, m.PendingUnit)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", compliance.State(m.State).Label(), id, orDash(m.OS), pend, m.Exploited,
			since(fleet.EvaluatedAt.Sub(m.LastSeenAt)), orDash(m.Hostname), strings.Join(m.Reasons, "; "))
	}
	_ = tw.Flush()
}

// since escreve há quanto tempo: minutos, horas ou dias.
func since(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	}
	return ago(d)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
