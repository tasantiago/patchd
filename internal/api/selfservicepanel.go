package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
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
	NeedsApproval         bool // Aula 18.3
	Groups, Updated       string
	Active                bool
	Selected              bool   // Aula 18.8: marcada para o lote (volta marcada se nada foi gravado)
	Problem               string // Aula 18.8: o problema desta linha, quando nada foi gravado
}

type ssItem struct {
	selfservice.Item
	Label string
}

type selfServiceView struct {
	NoManifest bool
	Rows       []ssOfferRow
	Machine    string // a máquina da prévia, como foi digitada (nome ou ID)
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
			row.Active, row.CanUpdate, row.CanInstall, row.NeedsApproval = true, o.CanUpdate, o.CanInstall, o.NeedsApproval
			row.Groups = strings.Join(o.Groups, ", ")
			row.Updated = a.stamp(o.UpdatedAt) + " · " + o.UpdatedBy
		}
		sv.Rows = append(sv.Rows, row)
	}
	slices.SortFunc(sv.Rows, func(x, y ssOfferRow) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })

	// Prévia: a loja de uma máquina Windows, para os grupos digitados.
	q := r.URL.Query()
	chosen, groups := q.Get("maquina"), strings.TrimSpace(q.Get("grupos"))
	groups = texto.Clip(groups, 2000)
	sv.Groups = groups
	// A máquina vem digitada (o nome ou o ID): a caixa de seleção com o parque inteiro eram
	// milhares de opções, meio megabyte de página a cada abertura da aba.
	chosen = texto.Clip(strings.TrimSpace(chosen), maxSearchLen)
	sv.Machine = chosen
	if chosen == "" {
		return nil
	}
	machines, err := a.store.Machines(ctx)
	if err != nil {
		return err
	}
	chosen, sv.Note = pickWindowsMachine(machines, chosen)
	if chosen == "" {
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
	for _, it := range selfservice.Catalog(offers, selfservice.TokenMember(splitGroups(groups)), selfservice.Machine{Software: inv.Software, Arch: arch},
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
	r.Body = http.MaxBytesReader(w, r.Body, ssBatchMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	if r.PostForm.Has("modo") { // Aula 18.8: a tabela inteira, ou o lote
		a.selfServiceBatchAction(w, r, s)
		return
	}
	app := r.PostFormValue("app")
	if a.tpManifest == nil || !slices.ContainsFunc(a.tpManifest.For("windows"), func(x thirdparty.App) bool { return x.ID == app && x.Install != nil }) {
		http.Redirect(w, r, back+"&erro=programa", http.StatusSeeOther)
		return
	}
	o := store.SelfServiceOffer{AppID: app, CanUpdate: r.PostFormValue("atualizar") == "1", CanInstall: r.PostFormValue("instalar") == "1",
		NeedsApproval: r.PostFormValue("aprovacao") == "1", Groups: splitGroups(r.PostFormValue("grupos")), UpdatedBy: s.Username}
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
	if o.NeedsApproval {
		detail += " · com aprovação da TI"
	}
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
		b.Flash = fmt.Sprintf("Oferta de %s gravada. Vale a partir do próximo pedido de um usuário.", q.Get("salvo"))
	case q.Has("lote") && q.Get("lote") == "":
		b.Flash = "Nada mudou: as ofertas já estavam assim."
	case q.Get("lote") != "":
		b.Flash = fmt.Sprintf("Ofertas gravadas: %s. Valem a partir do próximo pedido de um usuário.", q.Get("lote"))
	case q.Get("removido") != "":
		b.Flash = fmt.Sprintf("%s fora do autoatendimento.", q.Get("removido"))
	case q.Get("erro") == "programa":
		b.Error = "Esse programa não tem \"install\" nas regras de terceiros; nada mudou."
	case q.Get("erro") == "grupos":
		b.Error = fmt.Sprintf("Grupos: até %d, cada um pelo sAMAccountName (GG-Exemplo ou DOMINIO\\GG-Exemplo), separados por vírgula. Nada mudou.", store.MaxOfferGroups)
	}
}

// pickWindowsMachine acha a máquina Windows pelo que foi digitado: o ID inteiro, o nome
// inteiro (sem olhar a caixa) ou, se só uma casa, o começo do ID ou do nome. Sem uma só, o
// ID volta vazio e a nota diz por quê.
func pickWindowsMachine(machines []protocol.MachineSummary, typed string) (id, note string) {
	needle := strings.ToLower(typed)
	var exact, prefix []protocol.MachineSummary
	for _, m := range machines {
		if !strings.Contains(strings.ToLower(m.OSName), "windows") {
			continue
		}
		name := strings.ToLower(m.Hostname)
		switch {
		case m.ID == typed:
			return m.ID, ""
		case name == needle:
			exact = append(exact, m)
		case strings.HasPrefix(name, needle) || (len(needle) >= 4 && strings.HasPrefix(strings.ToLower(m.ID), needle)):
			prefix = append(prefix, m)
		}
	}
	found := exact
	if len(found) == 0 {
		found = prefix
	}
	switch len(found) {
	case 0:
		return "", "Nenhuma máquina Windows com esse nome ou ID. Digite o nome inteiro, o começo dele ou o começo do ID."
	case 1:
		return found[0].ID, ""
	}
	names := make([]string, 0, 5)
	for _, m := range found[:min(len(found), 5)] {
		names = append(names, dash(m.Hostname)+" ("+short(m.ID)+")")
	}
	more := ""
	if len(found) > 5 {
		more = fmt.Sprintf(" e mais %d", len(found)-5)
	}
	return "", fmt.Sprintf("%d máquinas Windows casam com %q: %s%s. Digite o nome inteiro ou o começo do ID.", len(found), typed, strings.Join(names, ", "), more)
}
