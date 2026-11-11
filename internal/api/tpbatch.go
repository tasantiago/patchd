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
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

// Patch › Terceiros em lote: a lista ganha filtros (nome, situação do instalador, itens por
// página) e a aprovação em lote da versão guardada mais nova de cada programa marcado. Mesmas
// duas etapas do lote do SO (patchbatch.go): conferir, depois aprovar o que se conferiu.

// Situação do instalador de um programa, para o filtro ?instalador=.
const (
	tpInstWaiting  = "aguardando" // a versão guardada mais nova não está aprovada
	tpInstApproved = "aprovado"   // a versão guardada mais nova está aprovada
	tpInstNone     = "sem"        // tem regra de instalação, mas nada guardado
	tpInstManual   = "manual"     // sem regra de instalação: o patchd não instala
)

var tpInstFilters = []struct{ V, L string }{
	{"", "Qualquer instalador"}, {tpInstWaiting, "Aguardando aprovação"}, {tpInstApproved, "Mais nova aprovada"},
	{tpInstNone, "Sem instalador guardado"}, {tpInstManual, "Sem regra de instalação"},
}

// tpQuery: o filtro da lista de terceiros.
type tpQuery struct {
	All       bool   // todos=1: também os programas sem problema
	Q         string // parte do nome
	Inst      string // um dos tpInst*
	Page, Per int
}

func parseTPQuery(v url.Values) tpQuery {
	q := tpQuery{All: v.Get("todos") == "1", Q: texto.Clip(strings.TrimSpace(v.Get("q")), maxSearchLen), Page: 1, Per: tpPerPage}
	if s := v.Get("instalador"); slices.ContainsFunc(tpInstFilters, func(o struct{ V, L string }) bool { return o.V == s }) {
		q.Inst = s
	}
	if n, _ := strconv.Atoi(v.Get("pagina")); n > 1 {
		q.Page = n
	}
	if n, _ := strconv.Atoi(v.Get("por")); slices.Contains(patchPerPageOptions, n) {
		q.Per = n
	}
	return q
}

func (q tpQuery) values() url.Values {
	v := url.Values{"aba": {"terceiros"}}
	if q.All {
		v.Set("todos", "1")
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if q.Inst != "" {
		v.Set("instalador", q.Inst)
	}
	if q.Per != tpPerPage {
		v.Set("por", strconv.Itoa(q.Per))
	}
	if q.Page > 1 {
		v.Set("pagina", strconv.Itoa(q.Page))
	}
	return v
}

func (q tpQuery) href() string { return "/painel/patch?" + q.values().Encode() }

// hidden: o filtro como campos ocultos do formulário do lote (Value é o nome, Label o valor).
func (q tpQuery) hidden() []optView {
	var out []optView
	for k, vs := range q.values() {
		if k != "aba" {
			out = append(out, optView{Value: k, Label: vs[0]})
		}
	}
	slices.SortFunc(out, func(a, b optView) int { return cmp.Compare(a.Value, b.Value) })
	return out
}

// tpInstaller: a situação do instalador de um programa.
type tpInstaller struct {
	State    string // tpInst*
	Newest   string // a versão guardada mais nova
	Approved string // a versão aprovada mais nova, se houver
}

// compareTPVersion: >0 se x for mais nova que y (a ordem dos instaladores guardados).
func compareTPVersion(x, y string) int {
	nx, _ := thirdparty.Normalize(x)
	ny, _ := thirdparty.Normalize(y)
	if c, err := version.CompareDotted(nx, ny); err == nil && c != 0 {
		return c
	}
	return cmp.Compare(x, y)
}

// tpInstallers: a situação do instalador de cada programa com regra de instalação. Programa
// fora do mapa: sem regra (tpInstManual).
func (a *api) tpInstallers(ctx context.Context) (map[string]tpInstaller, error) {
	out := map[string]tpInstaller{}
	if a.tpManifest == nil {
		return out, nil
	}
	for _, r := range a.tpManifest.For("windows") {
		if r.Install != nil {
			out[r.ID] = tpInstaller{State: tpInstNone}
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	pkgs, err := a.store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := a.store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, err
	}
	ok := map[string]bool{}
	for _, ap := range approvals {
		ok[store.TPApprovalKey(ap.AppID, ap.Version)] = true
	}
	for _, p := range pkgs {
		in, has := out[p.AppID]
		if !has {
			continue
		}
		if in.Newest == "" || compareTPVersion(p.Version, in.Newest) > 0 {
			in.Newest = p.Version
		}
		if ok[store.TPApprovalKey(p.AppID, p.Version)] && (in.Approved == "" || compareTPVersion(p.Version, in.Approved) > 0) {
			in.Approved = p.Version
		}
		out[p.AppID] = in
	}
	for id, in := range out {
		switch {
		case in.Newest == "":
			in.State = tpInstNone
		case in.Newest == in.Approved:
			in.State = tpInstApproved
		default:
			in.State = tpInstWaiting
		}
		out[id] = in
	}
	return out, nil
}

func tpInstallerOf(m map[string]tpInstaller, app string) tpInstaller {
	if in, ok := m[app]; ok {
		return in
	}
	return tpInstaller{State: tpInstManual}
}

// filterTP: os programas da lista, pelo filtro.
func filterTP(progs []tpProgram, inst map[string]tpInstaller, q tpQuery) []tpProgram {
	needle := strings.ToLower(q.Q)
	var out []tpProgram
	for _, p := range progs {
		if p.Problems == 0 && !q.All {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.Program), needle) && !strings.Contains(strings.ToLower(p.AppID), needle) {
			continue
		}
		if q.Inst != "" && tpInstallerOf(inst, p.AppID).State != q.Inst {
			continue
		}
		out = append(out, p)
	}
	return out
}

// tpSel é o valor de um programa no formulário do lote: programa e versão, como numa URL.
func tpSel(app, ver string) string { return url.Values{"app": {app}, "versao": {ver}}.Encode() }

func parseTPSel(v string) (app, ver string, ok bool) {
	q, err := url.ParseQuery(v)
	if err != nil || len(q["app"]) != 1 || len(q["versao"]) != 1 {
		return "", "", false
	}
	app, ver = q.Get("app"), q.Get("versao")
	return app, ver, app != "" && ver != "" && len(app) <= 64 && len(ver) <= 64
}

type tpBatchRow struct {
	Program, AppID, Version, Approved, Sel string
	Installs, Problems                     int
}

type tpBatchView struct {
	pageBase
	Rows         []tpBatchRow
	FilterTotal  int
	Max, MaxNote int
	Note, Back   string
	Skipped      int
	Hidden       []optView
}

// tpBatchAction: a conferência (sem confirmar=1) e a aprovação do lote de terceiros.
func (a *api) tpBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)
	if err := r.ParseForm(); err != nil {
		redirectPatch(w, r, "/painel/patch?aba=terceiros&erro=lote-grande")
		return
	}
	q := parseTPQuery(r.PostForm)
	back := q.href()
	if a.compliance == nil {
		redirectPatch(w, r, back)
		return
	}
	sels := r.PostForm["sel"]
	byFilter := r.PostFormValue("filtro") == "1"
	if !byFilter && len(sels) > maxApproveBatch {
		redirectPatch(w, r, withParams(back, "erro", "lote-grande"))
		return
	}
	ctx, cancel := a.complianceCtx(r)
	defer cancel()
	list, err := a.complianceItems(ctx)
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
	progs := aggregateThirdParty(list)
	byApp := map[string]tpProgram{}
	for _, p := range progs {
		byApp[p.AppID] = p
	}
	row := func(p tpProgram, ver string) tpBatchRow {
		return tpBatchRow{Program: p.Program, AppID: p.AppID, Version: ver, Approved: dash(tpInstallerOf(inst, p.AppID).Approved),
			Sel: tpSel(p.AppID, ver), Installs: len(p.Installs), Problems: p.Problems}
	}

	var chosen []tpBatchRow
	skipped := 0
	if byFilter {
		for _, p := range filterTP(progs, inst, q) {
			if in := tpInstallerOf(inst, p.AppID); in.State == tpInstWaiting {
				chosen = append(chosen, row(p, in.Newest))
			}
		}
	} else {
		pkgs, err := a.store.ThirdPartyPackages(r.Context())
		if err != nil {
			a.internalError(w, r, "listar instaladores", err)
			return
		}
		approvals, err := a.store.ThirdPartyApprovals(r.Context())
		if err != nil {
			a.internalError(w, r, "listar aprovações", err)
			return
		}
		stored, done := map[string]bool{}, map[string]bool{}
		for _, p := range pkgs {
			stored[store.TPApprovalKey(p.AppID, p.Version)] = true
		}
		for _, ap := range approvals {
			done[store.TPApprovalKey(ap.AppID, ap.Version)] = true
		}
		seen := map[string]bool{}
		for _, v := range sels {
			app, ver, ok := parseTPSel(v)
			k := store.TPApprovalKey(app, ver)
			if !ok || seen[k] {
				continue
			}
			seen[k] = true
			p, inFleet := byApp[app]
			if !inFleet || a.installRule(app) == nil || !stored[k] || done[k] {
				skipped++ // aprovado por outra pessoa, excluído, ou o programa saiu do parque
				continue
			}
			chosen = append(chosen, row(p, ver))
		}
	}

	if r.PostFormValue("confirmar") != "1" || byFilter {
		a.tpBatchConfirm(w, r, s, chosen, skipped, q, "", "")
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxNoteLen {
		a.tpBatchConfirm(w, r, s, chosen, skipped, q, note, fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen))
		return
	}
	approved, already := 0, 0
	rid := RequestID(r.Context())
	for _, c := range chosen {
		created, err := a.store.ApproveThirdParty(r.Context(), store.ThirdPartyApproval{AppID: c.AppID, Version: c.Version, ApprovedBy: s.Username, Note: note})
		if err != nil {
			a.internalError(w, r, "aprovar o instalador", err)
			return
		}
		if !created {
			already++
			continue
		}
		approved++
		a.logger.Info("instalador de terceiros aprovado", "app", c.AppID, "version", c.Version, "username", s.Username, "lote", true, "request_id", rid)
		a.audit(r, s, "terceiros.aprovar", c.AppID, c.Version)
	}
	redirectPatch(w, r, withParams(back, "lote", strconv.Itoa(approved), "ja", strconv.Itoa(already), "fora", strconv.Itoa(skipped)))
}

func (a *api) tpBatchConfirm(w http.ResponseWriter, r *http.Request, s panel.Session, chosen []tpBatchRow, skipped int,
	q tpQuery, note, errMsg string) {
	if len(chosen) == 0 {
		redirectPatch(w, r, withParams(q.href(), "erro", "lote-vazio"))
		return
	}
	v := tpBatchView{pageBase: a.baseFor(r.Context(), s, "Aprovar instaladores em lote", "patch"), Max: maxApproveBatch,
		MaxNote: maxNoteLen, Note: note, Back: q.href(), Skipped: skipped, Hidden: q.hidden()}
	v.Sub, v.Error = "terceiros", errMsg
	if len(chosen) > maxApproveBatch {
		v.FilterTotal = len(chosen)
		chosen = chosen[:maxApproveBatch]
	}
	v.Rows = chosen
	a.render(w, r, http.StatusOK, "patch-terceiros-lote", v)
}

// tpBatchFlash: a mensagem da lista de terceiros depois de um lote.
func tpBatchFlash(q url.Values) (flash, errMsg string) {
	switch q.Get("erro") {
	case "lote-vazio":
		return "", "Nenhum programa com versão guardada aguardando aprovação foi marcado."
	case "lote-grande":
		return "", fmt.Sprintf("Um lote tem no máximo %d programas.", maxApproveBatch)
	}
	if !q.Has("lote") {
		return "", ""
	}
	n := func(k string) int {
		v, err := strconv.Atoi(q.Get(k))
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	lote, ja, fora := n("lote"), n("ja"), n("fora")
	msg := fmt.Sprintf("%d %s em lote.", lote, plural(lote, "versão aprovada", "versões aprovadas"))
	if ja > 0 {
		msg += fmt.Sprintf(" %d já %s por outra pessoa; a aprovação original ficou.", ja, plural(ja, "estava aprovada", "estavam aprovadas"))
	}
	if fora > 0 {
		msg += fmt.Sprintf(" %d %s de fora: aprovada antes, excluída ou fora do parque.", fora, plural(fora, "ficou", "ficaram"))
	}
	return msg, ""
}
