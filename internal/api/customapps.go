package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// Programas incluídos pelo painel (Patch › Programas de terceiros › Incluir programa, migração
// 0074): um programa do Windows publicado no winget-pkgs vira uma regra do manifesto sem
// editar arquivo no servidor. O formulário se preenche pelo próprio winget (o nome, o
// fabricante, os tipos de instalador e como o programa aparece em Programas e Recursos), e a
// regra passa pela mesma conferência das do manifesto. Salvar troca o manifesto vivo
// (thirdparty.Live): o programa vale na hora para o painel, o autoatendimento, a padronização,
// as campanhas e a avaliação do parque. Depois é o caminho de sempre: baixar o instalador,
// aprovar a versão e oferecer no autoatendimento.

// WingetDescriber lê um pacote do winget-pkgs (winget.Client.Describe).
type WingetDescriber func(ctx context.Context, id string) (winget.Description, error)

// WithCustomApps liga a inclusão pelo painel. base é o manifesto sem os programas do banco
// (o embutido mais o local); describe preenche o formulário (nil: sem a busca); sync busca a
// versão de referência do programa salvo, em segundo plano (nil: espera a sincronização do
// catálogo).
func WithCustomApps(base thirdparty.Manifest, describe WingetDescriber, sync func(context.Context, thirdparty.App)) Option {
	return func(a *api) {
		a.custom = &customApps{base: base, describe: describe, sync: sync}
	}
}

type customApps struct {
	base     thirdparty.Manifest
	describe WingetDescriber
	sync     func(context.Context, thirdparty.App)
}

// customTimeout: a busca no winget (três arquivos pequenos e a lista de versões).
const customTimeout = 30 * time.Second

// customView é a página.
type customView struct {
	pageBase
	Rows    []customRow
	Form    customForm
	Upload  bool   // o formulário do instalador próprio (?proprio=1)
	Editing string // o id do programa em edição ("" = novo)
	Found   *winget.Description
	Types   []string
	CanUse  bool // o painel pode incluir (o servidor tem o manifesto vivo)
	CanFind bool // a busca no winget está ligada
	// O programa que acabou de ser salvo: o botão para a página dele (baixar ou enviar o
	// instalador, aprovar).
	SavedHref, SavedName string
}

type customRow struct {
	ID, Name, Winget, Match, Updated, Href string
	Upload                                 bool
}

// customForm: os campos, como digitados.
type customForm struct {
	Name, Winget, Inventory, Mode, Publisher, Type, Signer, Unsigned, Processes string
	NoSignature                                                                 bool
	// O atalho na área de trabalho de todos os usuários.
	ShortName, ShortTarget, ShortIcon string
}

// unquote desfaz o regexp.QuoteMeta (o painel monta a expressão; editar mostra o texto).
var quoted = regexp.MustCompile(`\\(.)`)

func unquote(s string) string { return quoted.ReplaceAllString(s, "$1") }

// formOf: o formulário de uma regra incluída.
func formOf(r thirdparty.App) customForm {
	f := customForm{Name: r.Name, Mode: thirdparty.NameEquals}
	m := strings.TrimPrefix(r.Match, "^")
	switch {
	case strings.HasSuffix(m, "( |$)"):
		f.Inventory, f.Mode = unquote(strings.TrimSuffix(m, "( |$)")), thirdparty.NamePrefix
	default:
		f.Inventory = unquote(strings.TrimSuffix(m, "$"))
	}
	if r.Publisher != "" {
		f.Publisher = unquote(strings.TrimSuffix(strings.TrimPrefix(r.Publisher, "^"), "$"))
	}
	if r.Install != nil {
		f.Winget, f.Type, f.Signer, f.Unsigned = r.Install.Winget, r.Install.Type, r.Install.Signer, r.Install.UnsignedReason
		f.Processes = strings.Join(r.Install.Processes, ", ")
		f.NoSignature = r.Install.Signer == ""
	}
	if r.Shortcut != nil {
		f.ShortName, f.ShortTarget, f.ShortIcon = r.Shortcut.Name, r.Shortcut.Target, r.Shortcut.Icon
	}
	return f
}

// input: o formulário enviado.
func customInput(r *http.Request) (customForm, thirdparty.CustomInput) {
	f := customForm{Name: strings.TrimSpace(r.PostFormValue("nome")), Winget: strings.TrimSpace(r.PostFormValue("winget")),
		Inventory: strings.TrimSpace(r.PostFormValue("inventario")), Mode: r.PostFormValue("modo"),
		Publisher: strings.TrimSpace(r.PostFormValue("fabricante")), Type: r.PostFormValue("tipo"),
		Signer: strings.TrimSpace(r.PostFormValue("assinante")), Unsigned: strings.TrimSpace(r.PostFormValue("sem_assinatura")),
		Processes: strings.TrimSpace(r.PostFormValue("processos")), NoSignature: r.PostFormValue("assinatura") == "nao",
		ShortName: strings.TrimSpace(r.PostFormValue("atalho_nome")), ShortTarget: strings.TrimSpace(r.PostFormValue("atalho_executavel")),
		ShortIcon: strings.TrimSpace(r.PostFormValue("atalho_icone"))}
	in := thirdparty.CustomInput{Name: f.Name, WingetID: f.Winget, InventoryName: f.Inventory, NameMode: f.Mode, Publisher: f.Publisher,
		InstallType: f.Type, Upload: r.PostFormValue("origem") == "proprio",
		ShortcutName: f.ShortName, ShortcutTarget: f.ShortTarget, ShortcutIcon: f.ShortIcon}
	if in.Upload {
		in.WingetID, f.Winget = "", ""
	}
	if f.NoSignature {
		in.UnsignedReason = f.Unsigned
	} else {
		in.Signer = thirdparty.SignerOrganization(f.Signer)
		f.Signer = in.Signer
	}
	for _, p := range strings.FieldsFunc(f.Processes, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' }) {
		if !slices.Contains(in.Processes, p) {
			in.Processes = append(in.Processes, p)
		}
	}
	return f, in
}

// customList: os programas incluídos, com a regra já conferida.
func (a *api) customList(ctx context.Context) ([]store.CustomApp, map[string]thirdparty.App, error) {
	list, err := a.store.CustomApps(ctx)
	if err != nil {
		return nil, nil, err
	}
	rules := map[string]thirdparty.App{}
	for _, c := range list {
		var app thirdparty.App
		if json.Unmarshal(c.Rule, &app) == nil {
			rules[c.ID] = app
		}
	}
	return list, rules, nil
}

// reloadRules troca o manifesto vivo: a base mais os programas do banco.
func (a *api) reloadRules(ctx context.Context) error {
	list, err := a.store.CustomApps(ctx)
	if err != nil {
		return err
	}
	raw := make([]json.RawMessage, 0, len(list))
	for _, c := range list {
		raw = append(raw, c.Rule)
	}
	all, errs := thirdparty.WithCustom(a.custom.base, raw)
	for _, e := range errs {
		a.logger.Warn("programa de terceiros incluído pelo painel fora do manifesto", "error", e)
	}
	a.tpManifest.Set(all)
	return nil
}

// customPage: GET /painel/patch/terceiros/incluir. A lista dos incluídos e o formulário;
// ?editar=ID abre um para editar; ?winget=ID busca o pacote e preenche.
func (a *api) customPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := customView{pageBase: a.baseFor(r.Context(), s, "Incluir programa", "patch"), Types: append([]string{""}, wingetTypes...)}
	v.CanUse = a.custom != nil && a.tpManifest != nil
	v.Form.Mode = thirdparty.NameEquals
	if !v.CanUse {
		a.render(w, r, http.StatusOK, "terceiros-incluir", v)
		return
	}
	v.CanFind = a.custom.describe != nil
	list, rules, err := a.customList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os programas incluídos", err)
		return
	}
	for _, c := range list {
		rule := rules[c.ID]
		v.Rows = append(v.Rows, a.customRowOf(c, rule))
	}
	q := r.URL.Query()
	v.Upload = q.Get("proprio") == "1"
	if v.Upload {
		v.Types = thirdparty.UploadTypes
		v.Form.Type = thirdparty.UploadTypes[0]
	}
	switch {
	case q.Get("editar") != "":
		if rule, ok := rules[q.Get("editar")]; ok {
			v.Editing, v.Form, v.Upload = q.Get("editar"), formOf(rule), rule.Uploaded()
			if v.Upload {
				v.Types = thirdparty.UploadTypes
			}
		}
	case q.Get("winget") != "" && v.CanFind:
		id := strings.TrimSpace(q.Get("winget"))
		v.Form.Winget = id
		if !winget.ValidID(id) {
			v.Error = "O pacote do winget tem a forma Fabricante.Programa (ex.: VideoLAN.VLC). Procure em winget.run ou com \"winget search\"."
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), customTimeout)
		defer cancel()
		d, err := a.custom.describe(ctx, id)
		if err != nil {
			v.Error = "Não achei o pacote no winget-pkgs: " + err.Error()
			break
		}
		v.Found = &d
		v.Form.Name = d.Name
		v.Form.Inventory, v.Form.Publisher = d.DisplayName, d.DisplayPublisher
		if v.Form.Inventory == "" {
			v.Form.Inventory = d.Name
			v.Form.Mode = thirdparty.NamePrefix // sem o nome declarado: o começo do nome do pacote
		}
		if v.Form.Publisher == "" {
			v.Form.Publisher = d.Publisher
		}
		if len(d.Types) > 0 {
			v.Form.Type = d.Types[0]
		}
	}
	if id := q.Get("salvo"); id != "" {
		if rule := a.installRule(id); rule != nil {
			v.SavedHref, v.SavedName = tpProgramPath(id), rule.Name
		}
	}
	switch {
	case q.Has("salvo") && q.Get("proprio") == "1":
		v.Flash = "Programa salvo. Abra-o (botão abaixo) e envie o instalador (.msi ou .exe) com a versão; depois ofereça no autoatendimento (Configuração › Autoatendimento)."
	case q.Has("salvo"):
		v.Flash = "Programa salvo. Ele já vale no painel: abra-o (botão abaixo), baixe o instalador em Instaladores guardados, aprove a versão e ofereça no autoatendimento (Configuração › Autoatendimento)."
	case q.Has("excluido"):
		v.Flash = "Programa excluído. Os instaladores guardados continuam em disco até você apagá-los na página do programa (antes de excluir) ou com o package."
	}
	if e := q.Get("erro"); e != "" {
		v.Error = customErrors[e]
	}
	a.render(w, r, http.StatusOK, "terceiros-incluir", v)
}

// wingetTypes: os tipos de instalador que o patchd instala, na ordem do formulário.
var wingetTypes = []string{"msi", "wix", "nullsoft", "inno", "burn", "exe"}

var customErrors = map[string]string{
	"em-uso": "O programa está em uso: tem versão aprovada, oferta no autoatendimento ou está num grupo da Padronização. " +
		"Revogue as aprovações, tire a oferta e tire dos grupos antes de excluir.",
	"nao-encontrado": "Esse programa não foi incluído pelo painel (os do manifesto não se excluem por aqui).",
}

// customSaveAction: POST /painel/patch/terceiros/incluir. Inclui ou, com "editar", troca.
func (a *api) customSaveAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.custom == nil || a.tpManifest == nil {
		http.Redirect(w, r, "/painel/patch/terceiros/incluir", http.StatusSeeOther)
		return
	}
	f, in := customInput(r)
	editing := strings.TrimSpace(r.PostFormValue("editar"))
	if in.Upload && editing != "" {
		in.ID = editing // o id do instalador próprio vem do nome de quando foi incluído
	}
	fail := func(msg string) {
		v := customView{pageBase: a.baseFor(r.Context(), s, "Incluir programa", "patch"), Form: f, Editing: editing, Upload: in.Upload,
			Types: append([]string{""}, wingetTypes...), CanUse: true, CanFind: a.custom.describe != nil}
		if in.Upload {
			v.Types = thirdparty.UploadTypes
		}
		v.Error = msg
		if list, rules, err := a.customList(r.Context()); err == nil {
			for _, c := range list {
				v.Rows = append(v.Rows, a.customRowOf(c, rules[c.ID]))
			}
		}
		a.render(w, r, http.StatusUnprocessableEntity, "terceiros-incluir", v)
	}
	if in.Signer == "" && in.UnsignedReason == "" {
		fail("Diga a organização que assina o instalador (O= do certificado, ex.: VideoLAN) ou, sem assinatura, por quê.")
		return
	}
	rule, err := thirdparty.BuildCustom(in)
	if err != nil {
		fail("A regra não passou na conferência: " + err.Error())
		return
	}
	switch {
	case editing != "" && editing != rule.ID:
		fail("Para trocar o pacote do winget, exclua este programa e inclua o novo: o pacote define o programa.")
		return
	case editing != "" && a.isUploadedCustom(r.Context(), editing) != rule.Uploaded():
		fail("Um programa do winget não vira instalador próprio (nem o contrário): exclua e inclua de novo.")
		return
	case editing == "" && a.custom.base.Has(rule.Key()):
		fail(fmt.Sprintf("O programa %s já está no manifesto do patchd: não precisa incluir.", rule.ID))
		return
	}
	list, _, err := a.customList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os programas incluídos", err)
		return
	}
	if editing == "" && slices.ContainsFunc(list, func(c store.CustomApp) bool { return c.ID == rule.ID }) {
		fail(fmt.Sprintf("O programa %s já foi incluído: edite-o na lista.", rule.ID))
		return
	}
	data, err := json.Marshal(rule)
	if err != nil {
		a.internalError(w, r, "guardar o programa", err)
		return
	}
	if err := a.store.SaveCustomApp(r.Context(), store.CustomApp{ID: rule.ID, Rule: data, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "guardar o programa", err)
		return
	}
	if err := a.reloadRules(r.Context()); err != nil {
		a.internalError(w, r, "recarregar as regras", err)
		return
	}
	action := "terceiros.incluir"
	if editing != "" {
		action = "terceiros.editar"
	}
	sig := "assinatura " + rule.Install.Signer
	if rule.Install.Signer == "" {
		sig = "sem assinatura: " + rule.Install.UnsignedReason
	}
	origin := "winget " + rule.Install.Winget
	if rule.Uploaded() {
		origin = "instalador próprio " + rule.Install.Type
	}
	detail := fmt.Sprintf("%s · %s · nome %q (%s) · %s", rule.Name, origin, f.Inventory, f.Mode, sig)
	if sc := rule.Shortcut; sc != nil {
		detail += fmt.Sprintf(" · atalho %q → %s", sc.Name, sc.Target)
	}
	a.audit(r, s, action, rule.ID, detail)
	a.logger.Info("programa de terceiros incluído pelo painel", "app", rule.ID, "origem", origin, "username", s.Username,
		"request_id", RequestID(r.Context()))
	if a.custom.sync != nil && !rule.Uploaded() {
		go a.custom.sync(context.WithoutCancel(r.Context()), rule) // a versão de referência, já
	}
	q := url.Values{"salvo": {rule.ID}}
	if rule.Uploaded() {
		q.Set("proprio", "1")
	}
	http.Redirect(w, r, "/painel/patch/terceiros/incluir?"+q.Encode(), http.StatusSeeOther)
}

// customDeleteAction: POST /painel/patch/terceiros/incluidos/excluir. Recusa o programa em uso
// (versão aprovada, oferta no autoatendimento, grupo da Padronização): cada um é uma decisão
// registrada, desfeita primeiro.
func (a *api) customDeleteAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := "/painel/patch/terceiros/incluir"
	id := strings.TrimSpace(r.PostFormValue("id"))
	if a.custom == nil || a.tpManifest == nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	inUse, err := a.customInUse(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "conferir o uso do programa", err)
		return
	}
	if inUse {
		http.Redirect(w, r, back+"?erro=em-uso", http.StatusSeeOther)
		return
	}
	ok, err := a.store.DeleteCustomApp(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "excluir o programa", err)
		return
	}
	if !ok {
		http.Redirect(w, r, back+"?erro=nao-encontrado", http.StatusSeeOther)
		return
	}
	if err := a.reloadRules(r.Context()); err != nil {
		a.internalError(w, r, "recarregar as regras", err)
		return
	}
	a.audit(r, s, "terceiros.excluir_programa", id, "programa incluído pelo painel")
	http.Redirect(w, r, back+"?excluido="+url.QueryEscape(id), http.StatusSeeOther)
}

// customInUse: o programa tem versão aprovada, oferta no autoatendimento ou está num grupo da
// Padronização.
func (a *api) customInUse(ctx context.Context, id string) (bool, error) {
	approvals, err := a.store.ThirdPartyApprovals(ctx)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(approvals, func(x store.ThirdPartyApproval) bool { return x.AppID == id }) {
		return true, nil
	}
	offers, err := a.store.SelfServiceOffers(ctx)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(offers, func(o store.SelfServiceOffer) bool { return o.AppID == id }) {
		return true, nil
	}
	policies, err := a.store.StandardizationPolicies(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(policies, func(p store.StandardizationPolicy) bool { return slices.Contains(p.Apps, id) }), nil
}

// customRowOf: a linha da lista dos incluídos.
func (a *api) customRowOf(c store.CustomApp, rule thirdparty.App) customRow {
	row := customRow{ID: c.ID, Name: rule.Name, Match: formOf(rule).Inventory, Href: tpProgramPath(c.ID),
		Updated: a.stamp(c.UpdatedAt) + " · " + c.UpdatedBy, Upload: rule.Uploaded()}
	if rule.Install != nil {
		row.Winget = rule.Install.Winget
	}
	return row
}

// isUploadedCustom: o programa incluído (id) é de instalador próprio.
func (a *api) isUploadedCustom(ctx context.Context, id string) bool {
	_, rules, err := a.customList(ctx)
	return err == nil && rules[id].Uploaded()
}

// isCustom: o programa foi incluído pelo painel (a página do programa mostra editar e excluir).
func (a *api) isCustom(id string) bool {
	return a.custom != nil && a.tpManifest != nil && !a.custom.base.Has(id+"/windows") && a.tpManifest.Get().Has(id+"/windows")
}
