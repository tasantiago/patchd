package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

const dashboardPriorityLimit = 8

type dashboardBar struct {
	Label, Href, Color string
	N, Pct             int
}

type dashboardSignal struct {
	Label, Detail, Href, Color string
	N                          int
}

type dashboardMachine struct {
	Name, Href, State, Label, Reason, Seen, Pending string
	Exploited                                       int
	lastSeen                                        time.Time
}

type dashboardView struct {
	pageBase
	AsOf, EvaluatedAt                                         string
	InventoryOn, ComplianceOn, JobsOn                         bool
	Total, FleetTotal, OK, Exploited, Offline, JobTotal, More int
	KPIs                                                      []ovKPI
	States, Systems, Execution                                []dashboardBar
	Signals                                                   []dashboardSignal
	Priorities                                                []dashboardMachine
	Issues                                                    []string
	Show                                                      map[string]bool
	Options                                                   []dashboardOption
	EmptySelection, CampaignsOn                               bool
	Campaigns, Security, Communication                        []dashboardBar
	CampaignTotal                                             int
}

// homePage preserva links antigos com filtros da frota e retornos de ações.
// Após o login, a entrada sem filtros abre os dashboards; a frota tem uma rota própria.
func (a *api) homePage(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) > 0 && !r.URL.Query().Has("personalizar") {
		a.fleetPage(w, r)
		return
	}
	a.dashboardPage(w, r)
}

func (a *api) dashboardPage(w http.ResponseWriter, r *http.Request) {
	now := a.now()
	v := dashboardView{pageBase: a.base(r, "Início", "inicio"), AsOf: now.UTC().Format("02/01/2006 15:04") + " UTC"}
	a.dashboardPreferences(w, r, &v)
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	issue := func(area string, err error) {
		v.Issues = append(v.Issues, area+": dados indisponíveis nesta consulta.")
		a.logger.Error("dashboard: consulta indisponível", "area", area, "error", err, "request_id", RequestID(r.Context()))
	}

	if v.wants("indicadores", "sistemas", "comunicacao") {
		machines, err := a.store.Machines(ctx)
		if err != nil {
			issue("Inventário", err)
		} else {
			v.InventoryOn = true
			v.Total = len(machines)
			bySystem := map[string]int{}
			for _, m := range machines {
				if m.LastSeenAt.IsZero() || m.LastSeenAt.Before(now.Add(-onlineWithin)) {
					v.Offline++
				}
				bySystem[osFamily(m.OSName)]++
			}
			v.Communication = []dashboardBar{
				{Label: "Contato nas últimas 2 h", N: v.Total - v.Offline, Pct: pct(v.Total-v.Offline, v.Total), Color: "em_dia", Href: "/painel/inventario?status=online"},
				{Label: "Sem contato recente", N: v.Offline, Pct: pct(v.Offline, v.Total), Color: "sem_dado_recente", Href: "/painel/inventario?status=offline"},
			}
			for os, n := range bySystem {
				v.Systems = append(v.Systems, dashboardBar{Label: os, N: n, Pct: pct(n, v.Total), Color: "sistema"})
			}
			slices.SortFunc(v.Systems, func(a, b dashboardBar) int {
				if a.N != b.N {
					return b.N - a.N
				}
				return strings.Compare(a.Label, b.Label)
			})
			if len(v.Systems) > 6 {
				other := 0
				for _, row := range v.Systems[6:] {
					other += row.N
				}
				v.Systems = append(v.Systems[:6], dashboardBar{Label: "Outras famílias", N: other, Pct: pct(other, v.Total), Color: "sistema"})
			}
		}
		v.KPIs = append(v.KPIs,
			dashboardKPI("Equipamentos ativos", v.Total, v.InventoryOn, "cadastros não aposentados", "/painel/inventario", ""),
			dashboardKPI("Sem contato recente", v.Offline, v.InventoryOn, "último contato há mais de 2 h ou nunca recebido", "/painel/inventario?status=offline", "v-laranja"))

	}
	if v.wants("indicadores", "decisoes") {
		if open, unacked, err := a.store.PerfAlertCounts(ctx); err != nil {
			issue("Alertas de desempenho", err)
			v.KPIs = append(v.KPIs, dashboardKPI("Alertas abertos", 0, false, "desempenho dos equipamentos", "/painel/alertas", ""))
		} else {
			v.KPIs = append(v.KPIs, dashboardKPI("Alertas abertos", open, true, fmt.Sprintf("%d ainda não reconhecidos", unacked), "/painel/alertas", "v-laranja"))
			if unacked > 0 {
				v.Signals = append(v.Signals, dashboardSignal{Label: "Alertas para reconhecer", Detail: "Investigue CPU, memória e espaço em disco.", N: unacked, Href: "/painel/alertas", Color: "atencao"})
			}
		}
		if n, err := a.store.JobsFinishedSince(ctx, []string{jobs.StateFailed, jobs.StateRejected, jobs.StateExpired}, now.Add(-24*time.Hour)); err != nil {
			issue("Resultados de jobs", err)
			v.KPIs = append(v.KPIs, dashboardKPI("Jobs sem sucesso · 24 h", 0, false, "falhos, recusados ou expirados", "/painel/jobs", ""))
		} else {
			v.KPIs = append(v.KPIs, dashboardKPI("Jobs sem sucesso · 24 h", n, true, "terminados nas últimas 24 h", "/painel/jobs", "v-vermelho"))
			if n > 0 {
				v.Signals = append(v.Signals, dashboardSignal{Label: "Execuções para investigar", Detail: "Falhas, recusas ou expirações nas últimas 24 h.", N: n, Href: "/painel/jobs", Color: "erro"})
			}
		}
	}
	if v.wants("execucoes") {
		if counts, err := a.store.JobCounts(ctx, now.Add(-7*24*time.Hour)); err != nil {
			issue("Execuções dos últimos 7 dias", err)
		} else {
			v.JobsOn = true
			v.Execution, v.JobTotal = dashboardExecutions(counts)
		}
	}
	if v.wants("decisoes") {
		if failures, err := a.store.SourceFailures(ctx, ""); err != nil {
			issue("Fontes de atualizações", err)
		} else {
			seen := map[string]bool{}
			for _, f := range failures {
				if f.Source != "wua-offline" {
					seen[f.MachineID] = true
				}
			}
			if len(seen) > 0 {
				v.Signals = append(v.Signals, dashboardSignal{Label: "Equipamentos com busca falhando", Detail: "Uma busca sem resposta não comprova ausência de atualizações.", N: len(seen), Href: "/painel/patch?aba=geral", Color: "atencao"})
			}
		}
		if requests, err := a.store.SelfServiceRequests(ctx, store.RequestFilter{Waiting: true, Limit: 500}); err != nil {
			issue("Pedidos de autoatendimento", err)
		} else if len(requests) > 0 {
			detail := "Aguardando uma decisão da TI."
			if len(requests) == 500 {
				detail = "Pelo menos 500 pedidos aguardando; consulte a lista completa."
			}
			v.Signals = append(v.Signals, dashboardSignal{Label: "Pedidos aguardando a TI", Detail: detail, N: len(requests), Href: "/painel/autoatendimento", Color: "atencao"})
		}
	}
	if v.wants("campanhas", "decisoes") {
		if camps, err := a.store.Campaigns(ctx, 200); err != nil {
			issue("Campanhas", err)
		} else {
			paused := 0
			v.CampaignsOn = true
			v.CampaignTotal = len(camps)
			byState := map[string]int{}
			for _, c := range camps {
				byState[c.State]++
				if c.State == campaign.StatePaused {
					paused++
				}
			}
			for _, state := range []struct{ key, label, color string }{
				{campaign.StateRunning, "Em andamento", "desconhecido"},
				{campaign.StatePaused, "Pausadas", "reboot_pendente"},
				{campaign.StateDone, "Concluídas", "em_dia"},
				{campaign.StateCanceled, "Canceladas", "sem_dado_recente"},
			} {
				v.Campaigns = append(v.Campaigns, dashboardBar{Label: state.label, N: byState[state.key], Pct: pct(byState[state.key], len(camps)), Color: state.color})
			}
			if paused > 0 {
				v.Signals = append(v.Signals, dashboardSignal{Label: "Campanhas pausadas", Detail: "Entre as 200 campanhas mais recentes; confira o motivo antes de retomar.", N: paused, Href: "/painel/campanhas", Color: "atencao"})
			}
		}
	}

	// A avaliação opcional vem por último: catálogo indisponível não esconde os outros indicadores.
	if v.wants("indicadores", "conformidade", "seguranca", "prioridades") {
		if a.compliance == nil {
			v.Issues = append(v.Issues, "Conformidade: avaliação indisponível; exige PostgreSQL e catálogo configurados.")
		} else if fleet, err := a.compliance.Fleet(ctx); err != nil {
			if a.canceled(r, err) {
				return
			}
			issue("Conformidade", err)
		} else {
			v.ComplianceOn = true
			v.EvaluatedAt = fleet.EvaluatedAt.UTC().Format("02/01/2006 15:04") + " UTC"
			v.dashboardFleet(fleet)
		}
	}
	complianceSub := "nenhum equipamento avaliado"
	if v.FleetTotal > 0 {
		complianceSub = fmt.Sprintf("%d%% de %d equipamentos avaliados", pct(v.OK, v.FleetTotal), v.FleetTotal)
	}
	v.KPIs = append(v.KPIs,
		dashboardKPI("Em dia", v.OK, v.ComplianceOn && v.FleetTotal > 0, complianceSub, "/painel/frota?estado=em_dia", "v-verde"),
		dashboardKPI("Com exploração conhecida", v.Exploited, v.ComplianceOn, "equipamentos com correção explorada pendente", (patchQuery{Status: "exploradas"}).href(), "v-vermelho"))
	a.render(w, r, http.StatusOK, "inicio", v)
}

func dashboardKPI(label string, n int, known bool, sub, href, color string) ovKPI {
	value := "—"
	if known {
		value = strconv.Itoa(n)
		if n == 0 && (color == "v-vermelho" || color == "v-laranja") {
			color = "v-cinza"
		}
	} else {
		color = "v-cinza"
	}
	return ovKPI{Label: label, Value: value, Sub: sub, Href: href, Color: color}
}

func (v *dashboardView) dashboardFleet(fleet protocol.ComplianceFleet) {
	v.FleetTotal = len(fleet.Machines)
	counts := map[compliance.State]int{}
	missing := 0
	warnings := false
	for _, m := range fleet.Machines {
		st := compliance.State(m.State)
		if st.Rank() == len(compliance.States) {
			st = compliance.StateUnknown
		}
		counts[st]++
		if m.Pending > 0 || st == compliance.StateMissing {
			missing++
		}
		warnings = warnings || len(m.CatalogWarnings) > 0
		if m.Exploited > 0 {
			v.Exploited++
		}
		if st == compliance.StateOK && m.Exploited == 0 {
			continue
		}
		seen := "sem contato registrado"
		if !m.LastSeenAt.IsZero() {
			seen = ago(fleet.EvaluatedAt.Sub(m.LastSeenAt))
		}
		pending := "—"
		if m.Pending > 0 {
			unit := m.PendingUnit
			if unit == "" {
				unit = "itens"
			}
			pending = fmt.Sprintf("%d %s", m.Pending, compliance.Unit(m.Pending, unit))
		}
		name := m.Hostname
		if name == "" {
			name = short(m.MachineID)
		}
		v.Priorities = append(v.Priorities, dashboardMachine{Name: name, Href: machinePath(m.MachineID), State: string(st), Label: st.Label(),
			Reason: shortError(strings.Join(m.Reasons, " · "), 240), Seen: seen, Pending: pending, Exploited: m.Exploited, lastSeen: m.LastSeenAt})
	}
	v.OK = counts[compliance.StateOK]
	v.Security = []dashboardBar{
		{Label: "Com correções pendentes", N: missing, Pct: pct(missing, v.FleetTotal), Color: "faltando"},
		{Label: "Com exploração conhecida", N: v.Exploited, Pct: pct(v.Exploited, v.FleetTotal), Color: "faltando"},
		{Label: "Estado principal: reinício pendente", N: counts[compliance.StateReboot], Pct: pct(counts[compliance.StateReboot], v.FleetTotal), Color: "reboot_pendente"},
		{Label: "Avaliação desconhecida", N: counts[compliance.StateUnknown], Pct: pct(counts[compliance.StateUnknown], v.FleetTotal), Color: "desconhecido"},
	}
	for _, st := range compliance.States {
		v.States = append(v.States, dashboardBar{Label: st.Label(), N: counts[st], Pct: pct(counts[st], v.FleetTotal), Color: string(st), Href: "/painel/frota?estado=" + string(st)})
	}
	if warnings {
		v.Issues = append(v.Issues, "Catálogos: há fontes atrasadas na avaliação; o retrato pode não incluir as correções mais recentes.")
	}
	slices.SortFunc(v.Priorities, func(a, b dashboardMachine) int {
		if (a.Exploited > 0) != (b.Exploited > 0) {
			if a.Exploited > 0 {
				return -1
			}
			return 1
		}
		if d := compliance.State(a.State).Rank() - compliance.State(b.State).Rank(); d != 0 {
			return d
		}
		if d := a.lastSeen.Compare(b.lastSeen); d != 0 {
			return d
		}
		return strings.Compare(a.Href, b.Href)
	})
	if len(v.Priorities) > dashboardPriorityLimit {
		v.More = len(v.Priorities) - dashboardPriorityLimit
		v.Priorities = v.Priorities[:dashboardPriorityLimit]
	}
}

func dashboardExecutions(counts []store.JobCount) ([]dashboardBar, int) {
	rows := []dashboardBar{
		{Label: "Concluídos", Color: "em_dia"},
		{Label: "Em aberto", Color: "desconhecido"},
		{Label: "Sem sucesso", Color: "faltando"},
		{Label: "Adiados ou dispensados", Color: "reboot_pendente"},
		{Label: "Cancelados", Color: "sem_dado_recente"},
		{Label: "Outros estados", Color: "sem_dado_recente"},
	}
	total := 0
	for _, c := range counts {
		i := 5
		switch c.State {
		case jobs.StateDone:
			i = 0
		case jobs.StatePending, jobs.StateDelivered:
			i = 1
		case jobs.StateFailed, jobs.StateRejected, jobs.StateExpired:
			i = 2
		case jobs.StatePostponed, jobs.StateDismissed:
			i = 3
		case jobs.StateCanceled:
			i = 4
		}
		rows[i].N += c.N
		total += c.N
	}
	for i := range rows {
		rows[i].Pct = pct(rows[i].N, total)
	}
	return rows, total
}
