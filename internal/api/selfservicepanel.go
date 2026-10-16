package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
	"github.com/tasantiago/patchd/internal/version"
)

// Configuração › Autoatendimento (Aula 17.1): o que o usuário da máquina vai poder pedir
// sozinho, por programa de terceiros com "install" nas regras. A oferta libera o programa
// (atualizar o que está instalado e/ou instalar do zero) para grupos do AD; a versão é
// sempre a aprovada e guardada (14.5): a oferta não aprova nada. A prévia mostra a "loja"
// de uma máquina para uma lista de grupos, antes de existir o app do usuário (17.4).

type ssOfferRow struct {
	AppID, Name, Approved string
	CanUpdate, CanInstall bool
	Groups, Updated       string
	Active                bool
}

type ssMachineOption struct {
	ID, Label string
	Selected  bool
}

type ssItem struct {
	selfservice.Item
	Label string
}

type selfServiceView struct {
	NoManifest bool
	Rows       []ssOfferRow
	Machines   []ssMachineOption
	Groups     string
	Ran        bool
	Items      []ssItem
	Note       string
}

var ssActionLabels = map[string]string{
	selfservice.ActionUpdate: "Atualizar", selfservice.ActionInstall: "Instalar", selfservice.ActionCurrent: "em dia",
	selfservice.ActionInstalled: "instalado", selfservice.ActionBlocked: "indisponível",
}

func (a *api) selfServiceTab(r *http.Request, v *settingsView) error {
	sv := &selfServiceView{}
	v.SelfService = sv
	if a.tpManifest == nil {
		sv.NoManifest = true
		return nil
	}
	ctx := r.Context()
	offers, err := a.store.SelfServiceOffers(ctx)
	if err != nil {
		return err
	}
	approvals, err := a.store.ThirdPartyApprovals(ctx)
	if err != nil {
		return err
	}
	byApp := map[string]store.SelfServiceOffer{}
	for _, o := range offers {
		byApp[o.AppID] = o
	}
	for _, rule := range a.tpManifest.For("windows") {
		if rule.Install == nil {
			continue
		}
		row := ssOfferRow{AppID: rule.ID, Name: rule.Name, Approved: newestApproved(approvals, rule.ID)}
		if o, ok := byApp[rule.ID]; ok {
			row.Active, row.CanUpdate, row.CanInstall = true, o.CanUpdate, o.CanInstall
			row.Groups = strings.Join(o.Groups, ", ")
			row.Updated = a.stamp(o.UpdatedAt) + " · " + o.UpdatedBy
		}
		sv.Rows = append(sv.Rows, row)
	}
	slices.SortFunc(sv.Rows, func(x, y ssOfferRow) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })

	// Prévia: a loja de uma máquina Windows, para os grupos digitados.
	q := r.URL.Query()
	chosen, groups := q.Get("maquina"), strings.TrimSpace(q.Get("grupos"))
	if len(groups) > 2000 {
		groups = groups[:2000]
	}
	sv.Groups = groups
	machines, err := a.store.Machines(ctx)
	if err != nil {
		return err
	}
	valid := false
	for _, m := range machines {
		if !strings.Contains(strings.ToLower(m.OSName), "windows") {
			continue
		}
		label := m.Hostname
		if label == "" {
			label = short(m.ID)
		}
		sv.Machines = append(sv.Machines, ssMachineOption{ID: m.ID, Label: label, Selected: m.ID == chosen})
		valid = valid || m.ID == chosen
	}
	if !valid {
		return nil
	}
	inv, found, err := a.store.LatestInventory(ctx, chosen)
	if err != nil {
		return err
	}
	arch, ok := tpinstall.MachineArch(inv.OS.Arch)
	if !found || !ok {
		sv.Note = "Sem inventário do Windows (ou arquitetura desconhecida) nesta máquina."
		return nil
	}
	pkgs, err := a.store.ThirdPartyPackages(ctx)
	if err != nil {
		return err
	}
	sv.Ran = true
	for _, it := range selfservice.Catalog(offers, selfservice.ListMember(splitGroups(groups)), selfservice.Machine{Software: inv.Software, Arch: arch},
		*a.tpManifest, pkgs, approvals) {
		sv.Items = append(sv.Items, ssItem{Item: it, Label: ssActionLabels[it.Action]})
	}
	return nil
}

// newestApproved: a versão aprovada mais nova do programa ("—" sem nenhuma).
func newestApproved(list []store.ThirdPartyApproval, app string) string {
	best := ""
	for _, ap := range list {
		if ap.AppID != app {
			continue
		}
		if best == "" {
			best = ap.Version
			continue
		}
		v, ok1 := thirdparty.Normalize(ap.Version)
		b, ok2 := thirdparty.Normalize(best)
		if c, err := version.CompareDotted(v, b); ok1 && ok2 && err == nil && c > 0 {
			best = ap.Version
		}
	}
	return dash(best)
}

func splitGroups(s string) []string {
	var out []string
	for _, g := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ';' }) {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// selfServiceOfferAction grava (ou tira) a oferta de um programa. Nada marcado: sai do
// autoatendimento.
func (a *api) selfServiceOfferAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := "/painel/configuracao?aba=autoatendimento"
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	app := r.PostFormValue("app")
	if a.tpManifest == nil || !slices.ContainsFunc(a.tpManifest.For("windows"), func(x thirdparty.App) bool { return x.ID == app && x.Install != nil }) {
		http.Redirect(w, r, back+"&erro=programa", http.StatusSeeOther)
		return
	}
	o := store.SelfServiceOffer{AppID: app, CanUpdate: r.PostFormValue("atualizar") == "1", CanInstall: r.PostFormValue("instalar") == "1",
		Groups: splitGroups(r.PostFormValue("grupos")), UpdatedBy: s.Username}
	if !o.CanUpdate && !o.CanInstall {
		removed, err := a.store.DeleteSelfServiceOffer(r.Context(), app)
		if err != nil {
			a.internalError(w, r, "tirar do autoatendimento", err)
			return
		}
		if removed {
			a.audit(r, s, "autoatendimento.remover", app, "")
			a.logger.Info("autoatendimento: programa retirado", "app", app, "username", s.Username, "request_id", RequestID(r.Context()))
		}
		http.Redirect(w, r, back+"&removido="+url.QueryEscape(app), http.StatusSeeOther)
		return
	}
	if err := o.Validate(); err != nil {
		http.Redirect(w, r, back+"&erro=grupos&app="+url.QueryEscape(app), http.StatusSeeOther)
		return
	}
	if err := a.store.SetSelfServiceOffer(r.Context(), o); err != nil {
		a.internalError(w, r, "gravar a oferta", err)
		return
	}
	detail := fmt.Sprintf("atualizar %s · instalar %s · %d grupo(s)", yesNo(o.CanUpdate), yesNo(o.CanInstall), len(o.Groups))
	a.audit(r, s, "autoatendimento.oferta", app, detail)
	a.logger.Info("autoatendimento: oferta gravada", "app", app, "atualizar", o.CanUpdate, "instalar", o.CanInstall, "grupos", len(o.Groups),
		"username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, back+"&salvo="+url.QueryEscape(app), http.StatusSeeOther)
}

func yesNo(b bool) string {
	if b {
		return "sim"
	}
	return "não"
}

// selfServiceFlash: as mensagens da aba.
func selfServiceFlash(q url.Values, b *pageBase) {
	switch {
	case q.Get("salvo") != "":
		b.Flash = fmt.Sprintf("Oferta de %s gravada. Vale a partir do próximo pedido de um usuário (Aula 17.2).", q.Get("salvo"))
	case q.Get("removido") != "":
		b.Flash = fmt.Sprintf("%s fora do autoatendimento.", q.Get("removido"))
	case q.Get("erro") == "programa":
		b.Error = "Esse programa não tem \"install\" nas regras de terceiros; nada mudou."
	case q.Get("erro") == "grupos":
		b.Error = fmt.Sprintf("Grupos: até %d, cada um pelo sAMAccountName ou pelo DN (CN=...,DC=...), separados por vírgula. Nada mudou.", store.MaxOfferGroups)
	}
}
