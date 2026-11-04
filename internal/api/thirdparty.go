package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Patch › Terceiros (Aula 13.3): os programas de terceiros (Chrome, Firefox, 7-Zip...) que o
// manifesto da Aula 6.5 reconhece, juntados por programa na frota. Até aqui, essa avaliação
// só existia na linha de comando (patchd-server compliance -machine).
//
// Sem aprovação nem botão de atualizar: o patchd não instala programas de terceiros. A lista
// diz o que distribuir e onde; a distribuição continua pelo ITOM.

// Situações da avaliação (internal/thirdparty), como chegam no protocolo.
const (
	tpOutdated  = "DESATUALIZADO"
	tpBelowMin  = "ABAIXO DO MÍNIMO"
	tpEOL       = "FORA DE SUPORTE"
	tpBadVer    = "VERSÃO ILEGÍVEL"
	tpNoRef     = "SEM REFERÊNCIA"
	tpOSVendor  = "CATÁLOGO DO SO"
	tpCurrent   = "em dia"
	tpPerPage   = 25
	tpMaxShownV = 3 // versões instaladas mostradas na lista
)

// tpProblem: a situação pede ação (atualizar, remover ou acertar a referência).
func tpProblem(state string) bool {
	switch state {
	case tpOutdated, tpBelowMin, tpEOL, tpBadVer, tpNoRef:
		return true
	}
	return false
}

// tpClass: a cor do selo, reaproveitando as dos estados da frota.
func tpClass(state string) string {
	switch state {
	case tpEOL, tpBelowMin:
		return "faltando"
	case tpOutdated:
		return "reboot_pendente"
	case tpCurrent:
		return "em_dia"
	}
	return "desconhecido"
}

// tpRank: a ordem das situações na tela (o mais grave primeiro).
func tpRank(state string) int {
	switch state {
	case tpEOL:
		return 0
	case tpBelowMin:
		return 1
	case tpOutdated:
		return 2
	case tpBadVer, tpNoRef:
		return 3
	case tpOSVendor:
		return 4
	}
	return 5
}

// tpInstall: uma instalação do programa numa máquina.
type tpInstall struct {
	MachineID, Machine, Name, Installed, State, Scope string
}

// tpProgram: um programa na frota, com as instalações.
type tpProgram struct {
	AppID, Program, Reference, Origin string
	Installs                          []tpInstall
	Problems, EOL, Outdated           int
}

// aggregateThirdParty junta as instalações por programa. Ordem: com instalação fora de
// suporte primeiro, depois quem tem mais instalações com problema, depois o nome.
func aggregateThirdParty(list []protocol.MachineItems) []tpProgram {
	by := map[string]*tpProgram{}
	var order []string
	for _, m := range list {
		for _, t := range m.ThirdParty {
			p := by[t.AppID]
			if p == nil {
				p = &tpProgram{AppID: t.AppID, Program: t.Program}
				by[t.AppID] = p
				order = append(order, t.AppID)
			}
			if p.Reference == "" {
				p.Reference, p.Origin = t.Reference, t.Origin
			}
			p.Installs = append(p.Installs, tpInstall{MachineID: m.MachineID, Machine: m.Hostname, Name: t.Name,
				Installed: t.Installed, State: t.State, Scope: t.Scope})
			if tpProblem(t.State) {
				p.Problems++
			}
			switch t.State {
			case tpEOL:
				p.EOL++
			case tpOutdated, tpBelowMin:
				p.Outdated++
			}
		}
	}
	out := make([]tpProgram, 0, len(order))
	for _, k := range order {
		p := by[k]
		slices.SortFunc(p.Installs, func(a, b tpInstall) int {
			return cmp.Or(cmp.Compare(tpRank(a.State), tpRank(b.State)), cmp.Compare(strings.ToLower(a.Machine), strings.ToLower(b.Machine)),
				cmp.Compare(a.MachineID, b.MachineID), cmp.Compare(a.Name, b.Name))
		})
		out = append(out, *p)
	}
	slices.SortStableFunc(out, func(a, b tpProgram) int {
		return cmp.Or(cmp.Compare(b.EOL, a.EOL), cmp.Compare(b.Problems, a.Problems), cmp.Compare(strings.ToLower(a.Program), strings.ToLower(b.Program)))
	})
	return out
}

func tpProgramPath(appID string) string {
	return "/painel/patch/programa?" + url.Values{"app": {appID}}.Encode()
}

// --- Lista --------------------------------------------------------------------------

type tpRow struct {
	Program, Href, Reference, Origin, Versions string
	Installs, Problems, EOL, Outdated          int
	Installer, InstallerClass, InstallerNote   string
	Sel                                        string // admin e versão guardada aguardando: o valor do lote
}

type tpView struct {
	pageBase
	Unavailable                             string
	Programs, Outdated, EOL, Hosts, Waiting int
	All                                     bool // ?todos=1: também os programas sem problema
	Rows                                    []tpRow
	Shown, Prev, Next                       string
	// Filtro e lote.
	Q, Inst                string
	Insts, Pers            []optView
	HasFilter, CanBatch    bool
	Found, FilterWaiting   int
	MaxBatch               int
	Hidden                 []optView
	AllHref, ProblemsHref  string
	WaitingHref, ClearHref string
}

func (a *api) thirdPartyTab(w http.ResponseWriter, r *http.Request) {
	q := parseTPQuery(r.URL.Query())
	v := tpView{pageBase: a.base(r, "Patch · Terceiros", "patch"), All: q.All, Q: q.Q, Inst: q.Inst, MaxBatch: maxApproveBatch}
	v.Sub = "terceiros"
	v.Flash, v.Error = tpBatchFlash(r.URL.Query())
	for _, o := range tpInstFilters {
		v.Insts = append(v.Insts, optView{Value: o.V, Label: o.L, Selected: o.V == q.Inst})
	}
	for _, n := range patchPerPageOptions {
		v.Pers = append(v.Pers, optView{Value: strconv.Itoa(n), Label: strconv.Itoa(n), Selected: n == q.Per})
	}
	v.HasFilter = q.Q != "" || q.Inst != ""
	link := func(change func(*tpQuery)) string {
		c := q
		c.Page = 1
		change(&c)
		return c.href()
	}
	v.AllHref = link(func(c *tpQuery) { c.All = true })
	v.ProblemsHref = link(func(c *tpQuery) { c.All = false })
	v.WaitingHref = link(func(c *tpQuery) { c.All, c.Inst, c.Q = true, tpInstWaiting, "" })
	v.ClearHref = tpQuery{All: q.All, Per: q.Per}.href()
	if a.compliance == nil {
		v.Unavailable = "Os programas de terceiros exigem o PostgreSQL com o catálogo."
		a.render(w, r, http.StatusOK, "patch-terceiros", v)
		return
	}
	ctx, cancel := a.complianceCtx(r)
	defer cancel()
	list, err := a.compliance.Items(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar os programas de terceiros", err)
		return
	}
	inst, err := a.tpInstallers(r.Context())
	if err != nil {
		a.internalError(w, r, "listar instaladores", err)
		return
	}
	hosts := map[string]bool{}
	progs := aggregateThirdParty(list)
	for _, p := range progs {
		if p.Problems > 0 {
			v.Programs++
			for _, in := range p.Installs {
				if tpProblem(in.State) {
					hosts[in.MachineID] = true
				}
			}
		}
		v.Outdated += p.Outdated
		v.EOL += p.EOL
		if tpInstallerOf(inst, p.AppID).State == tpInstWaiting {
			v.Waiting++
		}
	}
	v.Hosts = len(hosts)
	shown := filterTP(progs, inst, q)
	v.Found = len(shown)
	for _, p := range shown {
		if tpInstallerOf(inst, p.AppID).State == tpInstWaiting {
			v.FilterWaiting++
		}
	}
	v.CanBatch = v.Admin && v.FilterWaiting > 0
	v.Hidden = q.hidden()

	page, pages := pageOf(strconv.Itoa(q.Page), len(shown), q.Per)
	from, to := (page-1)*q.Per, min(page*q.Per, len(shown))
	at := func(n int) string { c := q; c.Page = n; return c.href() }
	if len(shown) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(shown))
	}
	if page > 1 {
		v.Prev = at(page - 1)
	}
	if page < pages {
		v.Next = at(page + 1)
	}
	for _, p := range shown[from:to] {
		var vers []string
		for _, in := range p.Installs {
			if !slices.Contains(vers, in.Installed) {
				vers = append(vers, in.Installed)
			}
		}
		text := strings.Join(vers[:min(len(vers), tpMaxShownV)], ", ")
		if len(vers) > tpMaxShownV {
			text += fmt.Sprintf(" e mais %d", len(vers)-tpMaxShownV)
		}
		row := tpRow{Program: p.Program, Href: tpProgramPath(p.AppID), Reference: dash(p.Reference), Origin: p.Origin,
			Versions: text, Installs: len(p.Installs), Problems: p.Problems, EOL: p.EOL, Outdated: p.Outdated}
		in := tpInstallerOf(inst, p.AppID)
		switch in.State {
		case tpInstWaiting:
			row.Installer, row.InstallerClass = "aguardando "+in.Newest, "reboot_pendente"
			if in.Approved != "" {
				row.InstallerNote = "aprovada: " + in.Approved
			}
			if v.Admin {
				row.Sel = tpSel(p.AppID, in.Newest)
			}
		case tpInstApproved:
			row.Installer, row.InstallerClass = "aprovado "+in.Approved, "em_dia"
		case tpInstNone:
			row.Installer, row.InstallerClass = "nada guardado", "sem_dado_recente"
		default:
			row.Installer, row.InstallerNote = "", "o patchd não instala"
		}
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "patch-terceiros", v)
}

// complianceCtx: o prazo das avaliações da frota.
func (a *api) complianceCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), complianceTimeout)
}

// pageOf: a página pedida, entre 1 e a última.
func pageOf(raw string, n, per int) (page, pages int) {
	pages = max(1, (n+per-1)/per)
	page, _ = strconv.Atoi(raw)
	return min(max(page, 1), pages), pages
}

// --- Programa -----------------------------------------------------------------------

type tpInstallRow struct {
	tpInstall
	Label, Class, ScopeLabel string
}

type tpProgramView struct {
	pageBase
	P    tpProgram
	Rows []tpInstallRow
	// Instaladores guardados e aprovação (Aula 14.5); Install nil: a regra não tem install.
	Install  *tpInstallInfo
	Versions []tpVersionRow
	MaxNote  int
	// Aula 23.3: baixar pelo painel e o andamento do download deste programa.
	CanFetch  bool
	Kinds     []tpKindOption // Aula 23.5: os tipos além do da regra
	RuleType  string
	Fetch     *tpFetchStatus
	OtherBusy string // outro programa baixando agora (um download por vez)
}

func (a *api) thirdPartyProgramPage(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	v := tpProgramView{pageBase: a.base(r, "Programa", "patch")}
	v.Sub = "terceiros"
	v.Flash, v.Error = tpApprovalFlash(r.URL.Query())
	if app == "" || a.compliance == nil {
		a.render(w, r, http.StatusNotFound, "patch-nao-encontrado", v.pageBase)
		return
	}
	ctx, cancel := a.complianceCtx(r)
	defer cancel()
	list, err := a.compliance.Items(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar os programas de terceiros", err)
		return
	}
	all := aggregateThirdParty(list)
	i := slices.IndexFunc(all, func(p tpProgram) bool { return p.AppID == app })
	if i < 0 {
		a.render(w, r, http.StatusNotFound, "patch-nao-encontrado", v.pageBase)
		return
	}
	v.P = all[i]
	v.Title = v.P.Program
	for _, in := range v.P.Installs {
		v.Rows = append(v.Rows, tpInstallRow{tpInstall: in, Label: strings.ToLower(in.State), Class: tpClass(in.State), ScopeLabel: scopeLabel(in.Scope)})
	}
	if err := a.fillInstallers(r, &v); err != nil {
		a.internalError(w, r, "listar os instaladores guardados", err)
		return
	}
	a.render(w, r, http.StatusOK, "patch-programa", v)
}

func scopeLabel(s string) string {
	if s == "user" {
		return "usuário"
	}
	return "máquina"
}

// --- Na máquina ---------------------------------------------------------------------

// tpMachineRow: um programa de terceiros na aba Pendências da máquina.
type tpMachineRow struct {
	protocol.ThirdPartyItem
	Label, Class, ScopeLabel, Href string
}

// thirdPartyRows: os programas com problema, o mais grave primeiro, e quantos estão em dia.
func thirdPartyRows(list []protocol.ThirdPartyItem) (rows []tpMachineRow, current int) {
	for _, t := range list {
		if !tpProblem(t.State) {
			if t.State == tpCurrent {
				current++
			}
			continue
		}
		rows = append(rows, tpMachineRow{ThirdPartyItem: t, Label: strings.ToLower(t.State), Class: tpClass(t.State),
			ScopeLabel: scopeLabel(t.Scope), Href: tpProgramPath(t.AppID)})
	}
	slices.SortStableFunc(rows, func(a, b tpMachineRow) int {
		return cmp.Or(cmp.Compare(tpRank(a.State), tpRank(b.State)), cmp.Compare(strings.ToLower(a.Program), strings.ToLower(b.Program)))
	})
	return rows, current
}
