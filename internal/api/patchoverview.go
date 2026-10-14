package api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/jobs"
)

// Patch › Visão geral (Aula 15.1): o retrato do patch da frota numa página, para quem
// abre o painel de manhã. O que está em dia por anel e por sistema, o que espera decisão
// (aprovação, campanha pausada), o que a execução fez nos últimos 7 dias e o que pede
// atenção agora. Tudo vem das mesmas fontes das outras telas (a avaliação de agora, as
// aprovações, os jobs e as campanhas): nada é guardado à parte.

const (
	overviewJobsWindow = 7 * 24 * time.Hour
	overviewCampaigns  = 50
)

type ovKPI struct {
	Label, Value, Sub, Href, Color string
}

// ovRing: um anel, com as máquinas por estado e a parte em dia.
type ovRing struct {
	Ring                                     string
	Machines, OK, Missing, Reboot, Stale, NA int
	Pct                                      int
}

type ovSystem struct {
	OS                               string
	Machines, OK, Pending, Exploited int
	Pct                              int
}

type ovExec struct {
	Type                                   string
	Created, Done, Failed, Postponed, Open int
}

type ovAttention struct {
	Text, Href, Level string // Level: erro, atencao
}

type overviewView struct {
	pageBase
	Unavailable string
	KPIs        []ovKPI
	Rings       []ovRing
	Systems     []ovSystem
	Exec        []ovExec
	ExecTotal   ovExec
	Attention   []ovAttention
	EvaluatedAt string
}

// osFamily: o nome do sistema sem a versão ("Windows 11 Pro 25H2" → "Windows").
func osFamily(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return "sem inventário"
	}
	return f[0]
}

func pct(part, total int) int {
	if total == 0 {
		return 0
	}
	return part * 100 / total
}

func (a *api) patchOverview(w http.ResponseWriter, r *http.Request) {
	v := overviewView{pageBase: a.base(r, "Patch", "patch")}
	if a.compliance == nil {
		v.Unavailable = "A visão geral exige o PostgreSQL com o catálogo."
		a.render(w, r, http.StatusOK, "patch-geral", v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	fleet, err := a.compliance.Fleet(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar a frota", err)
		return
	}
	items, err := a.compliance.Items(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os itens pendentes", err)
		return
	}
	approvals, err := a.store.PatchApprovals(ctx)
	if err != nil {
		a.internalError(w, r, "listar aprovações", err)
		return
	}
	machines, err := a.store.Machines(ctx)
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	camps, err := a.store.Campaigns(ctx, overviewCampaigns)
	if err != nil {
		a.internalError(w, r, "listar campanhas", err)
		return
	}
	now := a.now()
	v.EvaluatedAt = fleet.EvaluatedAt.Format("02/01/2006 15:04") + " UTC"

	// Frota: por anel e por sistema.
	ringOf := map[string]int{}
	for _, m := range machines {
		ringOf[m.ID] = m.Ring
	}
	byRing, bySystem := map[int]*ovRing{}, map[string]*ovSystem{}
	for _, m := range fleet.Machines {
		ring, known := ringOf[m.MachineID]
		if !known {
			ring = -1 // avaliada mas fora da lista de máquinas (aposentada agora há pouco)
		}
		rr := byRing[ring]
		if rr == nil {
			rr = &ovRing{Ring: "—"}
			if known {
				rr.Ring = ringLabel(ring)
			}
			byRing[ring] = rr
		}
		fam := osFamily(m.OS)
		sr := bySystem[fam]
		if sr == nil {
			sr = &ovSystem{OS: fam}
			bySystem[fam] = sr
		}
		rr.Machines++
		sr.Machines++
		sr.Pending += m.Pending
		sr.Exploited += m.Exploited
		switch compliance.State(m.State) {
		case compliance.StateOK:
			rr.OK++
			sr.OK++
		case compliance.StateMissing:
			rr.Missing++
		case compliance.StateReboot:
			rr.Reboot++
		case compliance.StateStale:
			rr.Stale++
		default:
			rr.NA++
		}
	}
	rings := slices.Sorted(maps.Keys(byRing))
	if len(rings) > 0 && rings[0] == -1 { // as fora da lista por último
		rings = append(rings[1:], -1)
	}
	for _, ring := range rings {
		rr := byRing[ring]
		rr.Pct = pct(rr.OK, rr.Machines)
		v.Rings = append(v.Rings, *rr)
	}
	for _, fam := range slices.Sorted(maps.Keys(bySystem)) {
		sr := bySystem[fam]
		sr.Pct = pct(sr.OK, sr.Machines)
		v.Systems = append(v.Systems, *sr)
	}

	// Itens do sistema e programas de terceiros.
	agg := aggregateItems(items, approvals)
	expWaiting, waiting := 0, 0
	for _, p := range agg {
		if p.Approval == nil {
			waiting++
			if p.Exploited {
				expWaiting++
			}
		}
	}
	tpInstalls, eol := 0, 0
	tpPrograms := map[string]bool{}
	for _, m := range items {
		for _, t := range m.ThirdParty {
			if tpProblem(t.State) {
				tpInstalls++
				tpPrograms[t.AppID] = true
			}
			if t.State == tpEOL {
				eol++
			}
		}
	}

	// Execução nos últimos 7 dias, por tipo: contada no banco (Aula 15.4), exata em
	// qualquer volume.
	counts, err := a.store.JobCounts(ctx, now.Add(-overviewJobsWindow))
	if err != nil {
		a.internalError(w, r, "contar jobs", err)
		return
	}
	failed24, err := a.store.JobsFinishedSince(ctx, []string{jobs.StateFailed, jobs.StateRejected, jobs.StateExpired}, now.Add(-24*time.Hour))
	if err != nil {
		a.internalError(w, r, "contar jobs sem sucesso", err)
		return
	}
	byType := map[string]*ovExec{}
	for _, c := range counts {
		er := byType[c.Type]
		if er == nil {
			er = &ovExec{Type: typeLabel(c.Type)}
			byType[c.Type] = er
		}
		er.Created += c.N
		switch c.State {
		case jobs.StateDone:
			er.Done += c.N
		case jobs.StatePostponed, jobs.StateDismissed:
			er.Postponed += c.N
		case jobs.StateFailed, jobs.StateRejected, jobs.StateExpired:
			er.Failed += c.N
		case jobs.StatePending, jobs.StateDelivered:
			er.Open += c.N
		}
	}
	for _, t := range slices.Sorted(maps.Keys(byType)) {
		er := *byType[t]
		v.Exec = append(v.Exec, er)
		v.ExecTotal.Created += er.Created
		v.ExecTotal.Done += er.Done
		v.ExecTotal.Failed += er.Failed
		v.ExecTotal.Postponed += er.Postponed
		v.ExecTotal.Open += er.Open
	}

	running, paused := 0, []campaign.Campaign{}
	for _, c := range camps {
		switch c.State {
		case campaign.StateRunning:
			running++
		case campaign.StatePaused:
			paused = append(paused, c)
		}
	}

	total, ok := len(fleet.Machines), fleet.Counts[string(compliance.StateOK)]
	waitColor := "v-laranja"
	if expWaiting > 0 {
		waitColor = "v-vermelho"
	}
	v.KPIs = []ovKPI{
		{Label: "Máquinas em dia", Value: fmt.Sprintf("%d%%", pct(ok, total)), Sub: fmt.Sprintf("%d de %d", ok, total), Href: "/painel/?estado=em_dia", Color: "v-verde"},
		{Label: "Sistema: aguardando aprovação", Value: fmt.Sprint(waiting), Sub: fmt.Sprintf("%d com exploração conhecida · %d item(ns) pendente(s) no total", expWaiting, len(agg)),
			Href: (patchQuery{Status: "aguardando"}).href(), Color: waitColor},
		{Label: "Terceiros com problema", Value: fmt.Sprint(tpInstalls), Sub: fmt.Sprintf("instalações, em %d programa(s)", len(tpPrograms)), Href: "/painel/patch?aba=terceiros", Color: "v-laranja"},
		{Label: "Jobs nos últimos 7 dias", Value: fmt.Sprint(v.ExecTotal.Created), Sub: fmt.Sprintf("%d concluídos · %d sem sucesso · %d adiados", v.ExecTotal.Done, v.ExecTotal.Failed, v.ExecTotal.Postponed), Href: "/painel/jobs"},
		{Label: "Campanhas em andamento", Value: fmt.Sprint(running), Sub: fmt.Sprintf("%d pausada(s)", len(paused)), Href: "/painel/campanhas"},
	}

	// O que pede atenção, o mais grave primeiro.
	if expWaiting > 0 {
		v.Attention = append(v.Attention, ovAttention{Level: "erro", Href: (patchQuery{Status: "exploradas"}).href(),
			Text: fmt.Sprintf("%d item(ns) com exploração conhecida aguardando aprovação", expWaiting)})
	}
	for _, c := range paused {
		v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: fmt.Sprintf("/painel/campanhas/%d", c.ID),
			Text: fmt.Sprintf("Campanha %d (%s) pausada: %s", c.ID, c.Name, c.Detail)})
	}
	if failed24 > 0 {
		v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: "/painel/jobs",
			Text: fmt.Sprintf("%d job(s) sem sucesso nas últimas 24 h", failed24)})
	}
	for _, s := range []struct {
		st   compliance.State
		text string
	}{
		{compliance.StateReboot, "%d máquina(s) com reinício pendente"},
		{compliance.StateStale, "%d máquina(s) sem contato recente"},
	} {
		if n := fleet.Counts[string(s.st)]; n > 0 {
			v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: "/painel/?estado=" + string(s.st), Text: fmt.Sprintf(s.text, n)})
		}
	}
	if eol > 0 {
		v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: "/painel/patch?aba=terceiros",
			Text: fmt.Sprintf("%d instalação(ões) de programa fora de suporte", eol)})
	}
	a.render(w, r, http.StatusOK, "patch-geral", v)
}
