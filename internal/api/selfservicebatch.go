package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Oferta em lote (Aula 18.8). A tabela de Configuração › Autoatendimento virou um formulário
// só: "Salvar alterações" grava o que está na tela e "Aplicar" põe a mesma regra (atualizar,
// instalar, aprovação da TI, grupos; cada um com "manter") nos programas marcados ou em todos,
// por cima do que está na tela. Só as linhas que mudaram são gravadas e auditadas, uma a uma,
// com um resumo do lote. Tudo ou nada na conferência: um grupo inválido em qualquer linha e
// nada é gravado; a página volta com o que foi digitado e o problema na linha.

// ssBatchMaxBody: a tabela inteira, com até 10 grupos por programa.
const ssBatchMaxBody = 512 << 10

// triState: "" mantém; "1" liga; "0" desliga.
func triState(v string, cur bool) bool {
	switch v {
	case "1":
		return true
	case "0":
		return false
	}
	return cur
}

func sameOffer(a, b store.SelfServiceOffer) bool {
	return a.CanUpdate == b.CanUpdate && a.CanInstall == b.CanInstall && a.NeedsApproval == b.NeedsApproval &&
		strings.Join(a.Groups, "\x00") == strings.Join(b.Groups, "\x00")
}

func (a *api) selfServiceBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := "/painel/configuracao?aba=autoatendimento"
	ctx := r.Context()
	if a.tpManifest == nil {
		http.Redirect(w, r, back+"&erro=programa", http.StatusSeeOther)
		return
	}
	known := map[string]string{}
	for _, rule := range a.tpManifest.For("windows") {
		if rule.Install != nil {
			known[rule.ID] = rule.Name
		}
	}
	var order []string
	desired := map[string]store.SelfServiceOffer{}
	for _, app := range r.PostForm["apps"] {
		if _, ok := known[app]; !ok {
			http.Redirect(w, r, back+"&erro=programa", http.StatusSeeOther)
			return
		}
		if _, dup := desired[app]; dup {
			continue
		}
		order = append(order, app)
		desired[app] = store.SelfServiceOffer{AppID: app, CanUpdate: r.PostFormValue("atualizar."+app) == "1",
			CanInstall: r.PostFormValue("instalar."+app) == "1", NeedsApproval: r.PostFormValue("aprovacao."+app) == "1",
			Groups: splitGroups(r.PostFormValue("grupos." + app)), UpdatedBy: s.Username}
	}
	selected := map[string]bool{}
	for _, app := range r.PostForm["sel"] {
		if _, ok := desired[app]; ok {
			selected[app] = true
		}
	}
	problem := ""
	problems := map[string]string{}
	if r.PostFormValue("modo") == "lote" {
		upd, inst, appr := r.PostFormValue("lote_atualizar"), r.PostFormValue("lote_instalar"), r.PostFormValue("lote_aprovacao")
		replaceGroups := r.PostFormValue("lote_grupos_modo") == "trocar"
		groups := splitGroups(r.PostFormValue("lote_grupos"))
		if r.PostFormValue("lote_alvo") == "todos" {
			for _, app := range order {
				selected[app] = true
			}
		}
		switch {
		case upd == "" && inst == "" && appr == "" && !replaceGroups:
			problem = "Em lote: escolha o que aplicar (cada campo começa em \"manter\"). Nada foi gravado."
		case len(selected) == 0:
			problem = "Em lote: marque ao menos um programa na primeira coluna, ou escolha \"todos os programas\". Nada foi gravado."
		}
		for app := range selected {
			o := desired[app]
			o.CanUpdate, o.CanInstall, o.NeedsApproval = triState(upd, o.CanUpdate), triState(inst, o.CanInstall), triState(appr, o.NeedsApproval)
			if replaceGroups {
				o.Groups = slices.Clone(groups)
			}
			desired[app] = o
		}
	}
	for _, app := range order {
		if o := desired[app]; o.CanUpdate || o.CanInstall {
			if err := o.Validate(); err != nil {
				problems[app] = fmt.Sprintf("Grupos: até %d, cada um pelo sAMAccountName (GG-Exemplo ou DOMINIO\\GG-Exemplo), separados por vírgula.", store.MaxOfferGroups)
			}
		}
	}
	if problem == "" && len(problems) > 0 {
		names := make([]string, 0, len(problems))
		for _, app := range order {
			if problems[app] != "" {
				names = append(names, known[app])
			}
		}
		problem = fmt.Sprintf("Grupos inválidos em %s. Nada foi gravado: corrija a linha marcada e salve de novo.", strings.Join(names, ", "))
	}
	if problem != "" {
		a.renderSelfServiceBatch(w, r, s, desired, selected, problems, problem)
		return
	}

	current, err := a.store.SelfServiceOffers(ctx)
	if err != nil {
		a.internalError(w, r, "ler as ofertas", err)
		return
	}
	byApp := map[string]store.SelfServiceOffer{}
	for _, o := range current {
		byApp[o.AppID] = o
	}
	var saved, removed []string
	for _, app := range order {
		want := desired[app]
		cur, active := byApp[app]
		wantActive := want.CanUpdate || want.CanInstall
		switch {
		case !wantActive && !active, wantActive && active && sameOffer(cur, want):
			continue
		case !wantActive:
			ok, err := a.store.DeleteSelfServiceOffer(ctx, app)
			if err != nil {
				a.internalError(w, r, "tirar do autoatendimento", err)
				return
			}
			if ok {
				a.audit(r, s, "autoatendimento.remover", app, "em lote")
				removed = append(removed, app)
			}
		default:
			if err := a.store.SetSelfServiceOffer(ctx, want); err != nil {
				a.internalError(w, r, "gravar a oferta", err)
				return
			}
			detail := fmt.Sprintf("atualizar %s · instalar %s · %d grupo(s)", yesNo(want.CanUpdate), yesNo(want.CanInstall), len(want.Groups))
			if want.NeedsApproval {
				detail += " · com aprovação da TI"
			}
			a.audit(r, s, "autoatendimento.oferta", app, detail+" · em lote")
			saved = append(saved, app)
		}
	}
	n := len(saved) + len(removed)
	if n > 0 {
		a.audit(r, s, "autoatendimento.oferta_lote", fmt.Sprintf("%d programa(s)", n),
			fmt.Sprintf("%d gravado(s), %d fora da loja", len(saved), len(removed)))
		a.logger.Info("autoatendimento: ofertas gravadas em lote", "gravados", len(saved), "retirados", len(removed),
			"username", s.Username, "request_id", RequestID(ctx))
	}
	names := make([]string, 0, n)
	for _, app := range append(saved, removed...) {
		names = append(names, known[app])
	}
	http.Redirect(w, r, back+"&lote="+url.QueryEscape(strings.Join(names, ", ")), http.StatusSeeOther)
}

// renderSelfServiceBatch devolve a aba com o que foi digitado (e o lote já aplicado) e o
// problema em cada linha: nada foi gravado.
func (a *api) renderSelfServiceBatch(w http.ResponseWriter, r *http.Request, s panel.Session, desired map[string]store.SelfServiceOffer,
	selected map[string]bool, problems map[string]string, problem string) {
	v := settingsView{pageBase: a.baseFor(s, "Configuração", "configuracao"), Tab: "autoatendimento",
		MaxUses: panelTokenMaxUses, MaxNote: panelTokenMaxNote, TTLs: hourOptions(tokenTTLs, 24)}
	if err := a.selfServiceTab(r, &v); err != nil {
		a.internalError(w, r, "montar a configuração", err)
		return
	}
	for i := range v.SelfService.Rows {
		row := &v.SelfService.Rows[i]
		if o, ok := desired[row.AppID]; ok {
			row.CanUpdate, row.CanInstall, row.NeedsApproval = o.CanUpdate, o.CanInstall, o.NeedsApproval
			row.Groups = strings.Join(o.Groups, ", ")
		}
		row.Selected = selected[row.AppID]
		row.Problem = problems[row.AppID]
	}
	v.Error = problem
	a.render(w, r, http.StatusUnprocessableEntity, "configuracao", v)
}
