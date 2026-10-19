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

// ovFix: uma correção aguardando aprovação (Aula 15.7). No Windows e no macOS, o item é a
// CVE e a correção é o build ou KB que a resolve: uma atualização acumulada fecha centenas
// de CVEs de uma vez. No Linux, o item é o pacote e cada pacote tem a sua versão corrigida.
type ovFix struct {
	FixedIn, Kind, Systems     string
	Items, Exploited, Machines int
	Href                       string
	Whole                      bool // CVEs de um build: dá para aprovar a correção inteira (Aula 15.9)
}

// maxOverviewFixes: as correções que a visão geral mostra, as que fecham mais itens antes.
const maxOverviewFixes = 10

// concentration: a partir de quantos itens aguardando uma máquina que concentra a maior
// parte vira aviso (uma máquina esquecida infla o número da frota inteira).
const (
	concentrationMin = 50
	concentrationPct = 50
)

type ovAttention struct {
	Text, Href, Level string // Level: erro, atencao
}

type overviewView struct {
	pageBase
	Unavailable string
	KPIs        []ovKPI
	Rings       []ovRing
	Systems     []ovSystem
	Fixes       []ovFix
	MoreFixes   int // correções além das mostradas
	FixesHref   string
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
	v.Sub = "geral"
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
	fx := waitingFixes(agg)
	v.Fixes, v.FixesHref = fx.list, (patchQuery{Status: "aguardando"}).href()
	if len(v.Fixes) > maxOverviewFixes {
		v.MoreFixes = len(v.Fixes) - maxOverviewFixes
		v.Fixes = v.Fixes[:maxOverviewFixes]
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
		{Label: "Sistema: correções aguardando", Value: fmt.Sprint(len(fx.list)), Sub: fx.summary(waiting, expWaiting),
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
	if m, ok := fx.concentrated(waiting); ok {
		v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: "/painel/maquinas/" + m.ID + "?aba=pendencias",
			Text: fmt.Sprintf("%s concentra %d dos %d itens aguardando (%d%%): atualizar essa máquina muda o retrato da frota",
				dash(m.Name), m.N, waiting, pct(m.N, waiting))})
	}
	for _, c := range paused {
		v.Attention = append(v.Attention, ovAttention{Level: "atencao", Href: fmt.Sprintf("/painel/campanhas/%d", c.ID),
			Text: fmt.Sprintf("Campanha %d (%s) pausada: %s", c.ID, c.Name, c.Detail)})
	}
	// Aula 16.2: fontes de busca falhando (o WSUS recusando a máquina, o apt sem rede).
	failures, err := a.store.SourceFailures(ctx, "")
	if err != nil {
		a.internalError(w, r, "listar fontes de busca falhando", err)
		return
	}
	v.Attention = append(v.Attention, a.overviewSourceAttention(failures, now)...)
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

// fixSummary: as correções aguardando, quantos itens de cada tipo e por máquina.
type fixSummary struct {
	list      []ovFix
	cves, pkg int
	machines  map[string]*machineCount
}

type machineCount struct {
	ID, Name string
	N        int
}

// waitingFixes junta os itens sem aprovação por correção: CVE pelo build ou KB, pacote
// pelo próprio pacote e versão. Ordem: as que fecham mais itens, depois as exploradas.
func waitingFixes(agg []patchItem) fixSummary {
	fs := fixSummary{machines: map[string]*machineCount{}}
	byKey := map[string]*ovFix{}
	machinesOf := map[string]map[string]bool{}
	systemsOf := map[string]map[string]bool{}
	var order []string
	for _, p := range agg {
		if p.Approval != nil {
			continue
		}
		key := p.Kind + "\x00" + p.FixedIn
		if p.Kind != "CVE" {
			key += "\x00" + p.ID // pacote: uma correção por pacote
		}
		if p.Kind == "CVE" {
			fs.cves++
		} else {
			fs.pkg++
		}
		f := byKey[key]
		if f == nil {
			f = &ovFix{FixedIn: p.FixedIn, Kind: fixKind(p), Href: (patchQuery{Status: "aguardando", Q: p.FixedIn}).href(),
				Whole: p.Kind == "CVE" && p.FixedIn != ""}
			if p.FixedIn == "" {
				f.Href = (patchQuery{Status: "aguardando"}).href()
			}
			byKey[key] = f
			machinesOf[key], systemsOf[key] = map[string]bool{}, map[string]bool{}
			order = append(order, key)
		}
		f.Items++
		if p.Exploited {
			f.Exploited++
		}
		for _, m := range p.Machines {
			machinesOf[key][m.ID] = true
			systemsOf[key][osFamily(m.OS)] = true
			mc := fs.machines[m.ID]
			if mc == nil {
				mc = &machineCount{ID: m.ID, Name: m.Name}
				fs.machines[m.ID] = mc
			}
			mc.N++
		}
	}
	for _, k := range order {
		f := byKey[k]
		f.Machines = len(machinesOf[k])
		f.Systems = strings.Join(slices.Sorted(maps.Keys(systemsOf[k])), ", ")
		fs.list = append(fs.list, *f)
	}
	slices.SortStableFunc(fs.list, func(a, b ovFix) int {
		if a.Items != b.Items {
			return b.Items - a.Items
		}
		if a.Exploited != b.Exploited {
			return b.Exploited - a.Exploited
		}
		return strings.Compare(a.FixedIn, b.FixedIn)
	})
	return fs
}

// fixKind: como a correção aparece na tabela.
func fixKind(p patchItem) string {
	if p.Kind == "CVE" {
		return "atualização do sistema"
	}
	return "pacote " + p.ID
}

// summary: a linha de baixo do cartão. "1006 itens (980 CVEs, 26 pacotes) em 3 máquinas".
func (fs fixSummary) summary(waiting, exploited int) string {
	if waiting == 0 {
		return "nada aguardando aprovação"
	}
	plural := func(n int, um, varios string) string {
		if n == 1 {
			return "1 " + um
		}
		return fmt.Sprintf("%d %s", n, varios)
	}
	var parts []string
	if fs.cves > 0 {
		parts = append(parts, plural(fs.cves, "CVE", "CVEs"))
	}
	if fs.pkg > 0 {
		parts = append(parts, plural(fs.pkg, "pacote", "pacotes"))
	}
	out := fmt.Sprintf("%d item(ns) (%s) em %d máquina(s)", waiting, strings.Join(parts, ", "), len(fs.machines))
	if exploited > 0 {
		out += fmt.Sprintf(" · %d com exploração conhecida", exploited)
	}
	return out
}

// concentrated: a máquina com a maior parte dos itens aguardando, quando o número é grande
// e uma máquina só responde por pelo menos concentrationPct dele.
func (fs fixSummary) concentrated(waiting int) (machineCount, bool) {
	if waiting < concentrationMin || len(fs.machines) < 2 {
		return machineCount{}, false
	}
	var top machineCount
	for _, m := range fs.machines {
		if m.N > top.N || (m.N == top.N && m.ID < top.ID) {
			top = *m
		}
	}
	return top, pct(top.N, waiting) >= concentrationPct
}
