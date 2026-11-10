package campaign

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Ligar pela rede na janela e desligar depois (docs/wake-on-lan.md, Aula 22.3).
const (
	// WakeLead: quanto antes do início da janela (ou do horário fixo) o pacote sai: a máquina
	// tem tempo de subir e fazer o check-in no começo da janela.
	WakeLead = 10 * time.Minute
	// WakeOffline: sem check-in há mais que isso, a máquina conta como desligada (o check-in é
	// de hora em hora). As ligadas não recebem pacote nem, depois, o desligar.
	WakeOffline = 75 * time.Minute
	// ShutdownTTL: o prazo do job desligar.
	ShutdownTTL = 2 * time.Hour
)

// wakeStep, a cada passo da campanha com Wake, no grupo atual:
//   - acorda as máquinas desligadas com job pendente cuja janela começa em até WakeLead (uma
//     vez por máquina e grupo);
//   - quando os jobs de uma máquina que ele acordou terminam (nenhum pendente, entregue ou
//     adiado), manda o desligar, se ela respondeu ao pacote e o agente sabe; senão, anota por
//     que não. O agente só desliga sem ninguém conectado.
func (e *Engine) wakeStep(ctx context.Context, c *Campaign, list []JobState, now time.Time) error {
	if e.Wake == nil {
		return nil
	}
	recs, err := e.Store.CampaignWakes(ctx, c.ID, c.CurrentIdx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, r := range recs {
		have[r.MachineID] = true
	}
	byMachine := map[string][]JobState{}
	for _, j := range list {
		byMachine[j.MachineID] = append(byMachine[j.MachineID], j)
	}
	by := fmt.Sprintf("campanha %d", c.ID)

	// 1. Acordar.
	var due []string
	for m, js := range byMachine {
		if have[m] {
			continue
		}
		if slices.ContainsFunc(js, func(j JobState) bool {
			return j.State == jobs.StatePending && j.NotBefore != nil && !now.Before(j.NotBefore.Add(-WakeLead))
		}) {
			due = append(due, m)
		}
	}
	slices.Sort(due)
	if len(due) > 0 {
		seen, err := e.Store.LastSeen(ctx, due)
		if err != nil {
			return err
		}
		woke, skipped := 0, 0
		for _, m := range due {
			if t, ok := seen[m]; ok && now.Sub(t) < WakeOffline {
				continue // ligada: o job sai no check-in dela
			}
			rid, problem, err := e.Wake(ctx, m, by)
			if err != nil {
				return err
			}
			if err := e.Store.SaveCampaignWake(ctx, WakeRecord{CampaignID: c.ID, Idx: c.CurrentIdx, MachineID: m, RequestedAt: now,
				WakeRequestID: rid, Problem: problem}); err != nil {
				return err
			}
			if rid != 0 {
				woke++
			} else {
				skipped++
			}
		}
		if woke+skipped > 0 {
			e.log(c, "ligar pela rede na janela", "anel", c.Ring(), "pedidos", woke, "sem_pedido", skipped)
		}
	}

	// 2. Desligar.
	for _, r := range recs {
		if r.WakeRequestID == 0 || r.ShutdownDecided() {
			continue
		}
		js := byMachine[r.MachineID]
		if slices.ContainsFunc(js, func(j JobState) bool { return !jobs.Final(j.State) || j.State == jobs.StatePostponed }) {
			continue // ainda trabalhando (ou o adiado vai ter nova tentativa)
		}
		switch ok, err := e.canShutdown(ctx, r.MachineID); {
		case err != nil:
			return err
		case r.AnsweredAt == nil:
			r.ShutdownNote = "não acordou: nenhum check-in depois do pacote"
		case !ok:
			r.ShutdownNote = "agente sem o desligar (atualize o agente)"
		default:
			rec, err := jobs.Issue(ctx, e.Store, e.Key, jobs.Request{MachineID: r.MachineID, Type: jobs.TypeShutdown, TTL: ShutdownTTL,
				By: by, Params: []byte("{}")}, now)
			switch {
			case errors.Is(err, jobs.ErrMachineUnavailable):
				r.ShutdownNote = "máquina aposentada"
			case err != nil:
				return err
			default:
				r.ShutdownJobID = rec.ID
				e.log(c, "desligar a máquina que o patchd ligou", "anel", c.Ring(), "machine_id", r.MachineID, "job", rec.ID)
			}
		}
		if err := e.Store.SaveCampaignWake(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) canShutdown(ctx context.Context, id string) (bool, error) {
	if e.CanShutdown == nil {
		return false, nil
	}
	return e.CanShutdown(ctx, id)
}
