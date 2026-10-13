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
}

type tpView struct {
	pageBase
	Unavailable                    string
	Programs, Outdated, EOL, Hosts int
	All                            bool // ?todos=1: também os programas sem problema
	Rows                           []tpRow
	Shown, Prev, Next              string
}

func (a *api) thirdPartyTab(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := tpView{pageBase: a.base(r, "Patch · Terceiros", "patch"), All: q.Get("todos") == "1"}
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
	hosts := map[string]bool{}
	var shown []tpProgram
	for _, p := range aggregateThirdParty(list) {
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
		if p.Problems > 0 || v.All {
			shown = append(shown, p)
		}
	}
	v.Hosts = len(hosts)

	page, pages := pageOf(q.Get("pagina"), len(shown), tpPerPage)
	from, to := (page-1)*tpPerPage, min(page*tpPerPage, len(shown))
	link := func(n int) string {
		u := url.Values{"aba": {"terceiros"}}
		if v.All {
			u.Set("todos", "1")
		}
		if n > 1 {
			u.Set("pagina", fmt.Sprint(n))
		}
		return "/painel/patch?" + u.Encode()
	}
	if len(shown) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(shown))
	}
	if page > 1 {
		v.Prev = link(page - 1)
	}
	if page < pages {
		v.Next = link(page + 1)
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
		v.Rows = append(v.Rows, tpRow{Program: p.Program, Href: tpProgramPath(p.AppID), Reference: dash(p.Reference), Origin: p.Origin,
			Versions: text, Installs: len(p.Installs), Problems: p.Problems, EOL: p.EOL, Outdated: p.Outdated})
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
}

func (a *api) thirdPartyProgramPage(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	v := tpProgramView{pageBase: a.base(r, "Programa", "patch")}
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
