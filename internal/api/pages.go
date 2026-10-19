package api

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/ui"
)

// As telas do painel (Aula 7.6): a frota, o detalhe de uma máquina e as aposentadas.
// Tudo é montado no servidor com html/template, que escapa cada valor conforme o lugar
// (texto, atributo, URL): nenhum nome de máquina ou motivo vira HTML. Sem JavaScript.

const (
	maxItemsShown   = 300 // pendências mostradas no detalhe (a API devolve todas)
	maxReasonLength = 200
	minReasonLength = 3
)

// pageBase é o que toda página tem: a sessão e o menu.
type pageBase struct {
	Brand   string // nome no topo (Aula 11.3)
	Title   string
	Session panel.Session
	Admin   bool
	Nav     string // o item do menu: frota, inventario, patch, campanhas, jobs, aneis, autoatendimento...
	Sub     string // Aula 19.1: o subitem (geral, so, terceiros em Patch; auditoria em Configuração)
	Flash   string // mensagem de sucesso (de uma lista fixa)
	Error   string // mensagem de erro (de uma lista fixa)
	// Aula 19.1: o resto do layout.
	Logo          string    // endereço do logotipo; vazio: o ícone padrão
	Badges        navCounts // os números do menu
	ServerVersion string
	AgentVersion  string // a versão publicada do agente
}

func (a *api) base(r *http.Request, title, nav string) pageBase {
	s, _ := SessionFrom(r.Context())
	return a.baseFor(r.Context(), s, title, nav)
}

// baseFor: a base de uma página montada por uma ação de admin (a sessão vem do
// adminAction, não do contexto).
func (a *api) baseFor(ctx context.Context, s panel.Session, title, nav string) pageBase {
	b := pageBase{Brand: a.brand, Title: title, Session: s, Admin: s.Role == panel.RoleAdmin, Nav: nav,
		ServerVersion: a.serverVersion, Logo: a.logoURL(ctx)}
	if b.ServerVersion == "" && a.system != nil {
		b.ServerVersion = a.system.Version
	}
	b.Badges, b.AgentVersion = a.chromeFor(ctx)
	return b
}

func (a *api) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	pageHeaders(w)
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		a.logger.Error("falha ao montar a página", "page", name, "error", err, "request_id", RequestID(r.Context()))
	}
}

// ago escreve há quanto tempo, como na linha de comando.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// --- Frota -------------------------------------------------------------------------

type countView struct {
	State, Label, Color string
	N                   int
	Active              bool
}

type fleetRow struct {
	ID, Short, Name, OS, State, Label, Pending, Seen, Ring string
	Exploited                                              int
	Reasons                                                []string
}

type fleetView struct {
	pageBase
	Unavailable string // sem estado de compliance (memória) ou avaliação com erro
	Counts      []countView
	Filter      string
	Total       int
	Rows        []fleetRow
	EvaluatedAt string
	Rings       []int // anéis oferecidos no lote (admin, Aula 11.1)
	FilterLabel string
	Exploited   int // máquinas com alguma pendência explorada (Aula 11.3)
	HTTP        int // máquinas cujo último check-in veio sem TLS
}

// stateColor: a cor do número no cartão de cada estado.
func stateColor(s compliance.State) string {
	switch s {
	case compliance.StateMissing:
		return "v-vermelho"
	case compliance.StateReboot, compliance.StateStale:
		return "v-laranja"
	case compliance.StateOK:
		return "v-verde"
	}
	return ""
}

// fleetPage lista a frota com o estado de cada máquina; ?estado= filtra.
func (a *api) fleetPage(w http.ResponseWriter, r *http.Request) {
	v := fleetView{pageBase: a.base(r, "Frota", "frota"), Rings: allRings()}
	fleetRingFlash(r.URL.Query(), &v.pageBase)
	filter := compliance.State(r.URL.Query().Get("estado"))
	if filter.Rank() < len(compliance.States) {
		v.Filter = string(filter)
	}
	if a.compliance == nil {
		v.Unavailable = "O estado de compliance exige o PostgreSQL com o catálogo."
		a.render(w, r, http.StatusOK, "frota", v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	fleet, err := a.compliance.Fleet(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.logger.Error("falha interna", "op", "avaliar a frota", "error", err, "request_id", RequestID(r.Context()))
		v.Unavailable = "A avaliação da frota falhou; veja o log do servidor."
		a.render(w, r, http.StatusOK, "frota", v)
		return
	}
	v.Total, v.EvaluatedAt = len(fleet.Machines), fleet.EvaluatedAt.Format("02/01/2006 15:04")+" UTC"
	// O anel de cada máquina (Aula 11.1): a avaliação não traz, a lista de máquinas traz.
	summaries, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	ringOf := make(map[string]int, len(summaries))
	for _, m := range summaries {
		ringOf[m.ID] = m.Ring
		if m.CheckinTLS != nil && !*m.CheckinTLS {
			v.HTTP++
		}
	}
	for _, m := range fleet.Machines {
		if m.Exploited > 0 {
			v.Exploited++
		}
	}
	for _, st := range compliance.States {
		v.Counts = append(v.Counts, countView{State: string(st), Label: st.Label(), Color: stateColor(st), N: fleet.Counts[string(st)], Active: v.Filter == string(st)})
		if v.Filter == string(st) {
			v.FilterLabel = st.Label()
		}
	}
	for _, m := range fleet.Machines {
		if v.Filter != "" && m.State != v.Filter {
			continue
		}
		pend := "—"
		if m.Pending > 0 {
			pend = fmt.Sprintf("%d %s", m.Pending, compliance.Unit(m.Pending, m.PendingUnit))
		}
		v.Rows = append(v.Rows, fleetRow{ID: m.MachineID, Short: short(m.MachineID), Name: dash(m.Hostname), OS: dash(m.OS),
			State: m.State, Label: compliance.State(m.State).Label(), Pending: pend, Exploited: m.Exploited,
			Seen: ago(fleet.EvaluatedAt.Sub(m.LastSeenAt)), Reasons: m.Reasons, Ring: ringText(ringOf, m.MachineID)})
	}
	a.render(w, r, http.StatusOK, "frota", v)
}

func ringText(ringOf map[string]int, id string) string {
	if r, ok := ringOf[id]; ok {
		return strconv.Itoa(r)
	}
	return "—"
}

// --- Detalhe da máquina ------------------------------------------------------------

type linkView struct {
	ID, Short, Relation string
	Retired             bool
}

type machineView struct {
	pageBase
	D                                    protocol.ComplianceDetail
	Name, Label, Seen, Collected, Reboot string
	Pending                              string
	Links                                []linkView
	Items                                []pendingRow // com a aprovação de cada uma (Aula 13.2)
	More                                 int
	MinReason, MaxReason                 int
	Jobs                                 jobTable
	JobsOn                               bool   // o servidor tem a chave: o admin pode pedir a busca
	Ring                                 string // "0 (piloto)", "1" (Aula 8.3)
	RingN                                int    // o anel em número, para o formulário (Aula 11.1)
	Rings                                []int
	CanUpdate                            bool           // admin, chave de jobs e pendências do Linux aprovadas (Aulas 8.4 e 13.2)
	NeedApproval                         bool           // há pendências do Linux, mas nenhuma aprovada
	ThirdParty                           []tpMachineRow // programas de terceiros com problema (Aula 13.3)
	ThirdPartyCurrent                    int
	TPUpdate                             *tpUpdateCard // o plano de terceiros e o botão Atualizar (Aula 14.6)
	CanReboot                            bool          // admin, chave, Linux com reinício pendente (Aula 8.6)
	Delays                               []int
	Window, NextWindow                   string
	Channel                              string // canal do último check-in (Aula 9.2)
	CanHTTPS                             bool   // admin, chave, URL pública e último check-in sem TLS
	PublicURL                            string
	Host                                 hostView       // rede e discos (Aula 12.1)
	SourceAlerts                         []sourceAlert  // fontes de busca falhando (Aula 16.2)
	WinUpdate                            *winUpdateCard // atualizações do Windows pelo patchd (Aula 16.3)
	// Abas (Aula 12.3).
	Tab           string
	Tabs          []tabView
	HasInventory  bool
	OSParams      []paramRow
	HWParams      []paramRow
	HWSources     []string
	Category      string
	Model, Serial string
	SoftwareCount int
	Software      softwareView
	Donuts        []donut
}

func relationLabel(r string) string {
	switch r {
	case "reenrollment":
		return "mesma instalação"
	case "clone":
		return "clone (mesma instalação, outro hardware)"
	case "same_hardware":
		return "mesmo hardware"
	}
	return r
}

// machinePage mostra o estado de uma máquina, as pendências e os alertas de identidade.
func (a *api) machinePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v := machineView{pageBase: a.base(r, "Máquina", "inventario"), MinReason: minReasonLength, MaxReason: maxReasonLength,
		Tab: machineTab(r.URL.Query())}
	if !machineIDPattern.MatchString(id) || a.compliance == nil {
		a.render(w, r, http.StatusNotFound, "nao-encontrada", v.pageBase)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	d, found, err := a.compliance.Detail(ctx, id)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		a.render(w, r, http.StatusNotFound, "nao-encontrada", v.pageBase)
		return
	}
	v.D, v.Name, v.Label = d, dash(d.Hostname), compliance.State(d.State).Label()
	v.Title = "Máquina " + short(d.MachineID)
	if d.Hostname != "" {
		v.Title = d.Hostname
	}
	v.Seen = ago(d.EvaluatedAt.Sub(d.LastSeenAt)) + " (" + d.LastSeenAt.Format("02/01/2006 15:04") + " UTC)"
	v.Collected = "—"
	if d.InventoryCollectedAt != nil {
		v.Collected = d.InventoryCollectedAt.Format("02/01/2006 15:04") + " UTC"
	}
	if d.Pending > 0 {
		v.Pending = fmt.Sprintf("%d %s (%d com exploração conhecida)", d.Pending, compliance.Unit(d.Pending, d.PendingUnit), d.Exploited)
	}
	approvals, err := a.store.PatchApprovals(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aprovações", err)
		return
	}
	v.Items = pendingRows(d.Items, approvals)
	v.ThirdParty, v.ThirdPartyCurrent = thirdPartyRows(d.ThirdParty)
	if len(v.Items) > maxItemsShown {
		v.More = len(v.Items) - maxItemsShown
		v.Items = v.Items[:maxItemsShown]
	}
	switch q := r.URL.Query(); {
	case q.Has("restaurada"):
		v.Flash = "Máquina devolvida à frota; a credencial dela voltou a valer."
	case q.Has("anel-alterado"):
		n, _ := strconv.Atoi(q.Get("anel"))
		v.Flash = fmt.Sprintf("Máquina no anel %d. Os jobs já criados não mudam; campanhas em andamento usam o anel novo nos próximos anéis que liberarem.", n)
	case q.Get("erro") == "anel":
		v.Error = fmt.Sprintf("Escolha um anel de 0 a %d.", store.MaxRing)
	case q.Get("erro") == "motivo":
		v.Error = fmt.Sprintf("Informe o motivo da aposentadoria (de %d a %d caracteres).", minReasonLength, maxReasonLength)
	default:
		jobFlash(q, &v.pageBase)
	}
	v.JobsOn = a.jobKey != nil
	if err := a.ringView(r, id, &v); err != nil {
		a.internalError(w, r, "ler anel e janela", err)
		return
	}
	if v.Admin && v.JobsOn {
		v.CanUpdate = slices.ContainsFunc(v.Items, func(it pendingRow) bool { return it.CanPick })
		v.NeedApproval = !v.CanUpdate && slices.ContainsFunc(v.Items, func(it pendingRow) bool { return len(it.Packages) > 0 })
		if v.CanReboot, err = a.canReboot(r, id); err != nil {
			a.internalError(w, r, "conferir reinício pendente", err)
			return
		}
		v.Delays = rebootDelays
	}
	if err := a.channelView(r, id, &v); err != nil {
		a.internalError(w, r, "ler canal do check-in", err)
		return
	}
	if err := a.hostView(r, id, &v); err != nil {
		a.internalError(w, r, "ler rede e discos", err)
		return
	}
	if v.SourceAlerts, err = a.machineSourceAlerts(r, id); err != nil {
		a.internalError(w, r, "ler as fontes de busca", err)
		return
	}
	if v.WinUpdate, err = a.winUpdateView(r.Context(), d, v.Admin); err != nil {
		a.internalError(w, r, "montar as atualizações do Windows", err)
		return
	}
	v.Donuts = donuts(v.Host.Disks)
	if err := a.inventoryView(r, id, &v); err != nil {
		a.internalError(w, r, "ler o inventário", err)
		return
	}
	if err := a.tpUpdateView(r, id, &v); err != nil {
		a.internalError(w, r, "montar o plano de terceiros", err)
		return
	}
	v.Jobs = jobTable{Admin: v.Admin, MachineID: d.MachineID}
	if v.Jobs.Rows, err = a.machineJobs(r, id, v.Admin); err != nil {
		a.internalError(w, r, "listar jobs da máquina", err)
		return
	}

	links, err := a.store.IdentityLinks(r.Context())
	if err != nil {
		a.internalError(w, r, "listar alertas de identidade", err)
		return
	}
	retired, err := a.store.RetiredMachines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aposentadas", err)
		return
	}
	isRetired := map[string]bool{}
	for _, m := range retired {
		isRetired[m.ID] = true
	}
	for _, l := range links {
		other := ""
		switch id {
		case l.MachineID:
			other = l.RelatedMachineID
		case l.RelatedMachineID:
			other = l.MachineID
		default:
			continue
		}
		v.Links = append(v.Links, linkView{ID: other, Short: short(other), Relation: relationLabel(l.Relation), Retired: isRetired[other]})
	}
	v.Tabs = tabsView(id, v.Tab, v.SoftwareCount, d.Pending+len(v.ThirdParty), len(v.Jobs.Rows))
	a.render(w, r, http.StatusOK, "maquina", v)
}

// --- Aposentadas -------------------------------------------------------------------

type retiredRow struct {
	ID, Short, Name, OS, When, By, Reason string
}

type retiredView struct {
	pageBase
	Rows []retiredRow
}

func (a *api) retiredPage(w http.ResponseWriter, r *http.Request) {
	v := retiredView{pageBase: a.base(r, "Máquinas aposentadas", "aposentadas")}
	if q := r.URL.Query(); q.Has("aposentada") {
		v.Flash = "Máquina aposentada: saiu da frota, e a credencial dela deixou de valer."
	}
	list, err := a.store.RetiredMachines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aposentadas", err)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RetiredAt.After(list[j].RetiredAt) })
	for _, m := range list {
		v.Rows = append(v.Rows, retiredRow{ID: m.ID, Short: short(m.ID), Name: dash(m.Hostname),
			OS: dash(strings.TrimSpace(m.OSName + " " + m.OSVersion)), When: m.RetiredAt.Format("02/01/2006 15:04") + " UTC",
			By: dash(m.RetiredBy), Reason: m.Reason})
	}
	a.render(w, r, http.StatusOK, "aposentadas", v)
}

// --- Ações do admin ----------------------------------------------------------------

// adminAction exige sessão com o perfil admin. Leitura recebe 403, e a tentativa vai
// para o log (alguém montou o pedido à mão: o botão nem aparece para esse perfil).
func (a *api) adminAction(next func(http.ResponseWriter, *http.Request, panel.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, _, ok, err := a.currentSession(r)
		if err != nil {
			a.internalError(w, r, "conferir sessão", err)
			return
		}
		if !ok {
			http.Redirect(w, r, "/painel/entrar", http.StatusSeeOther)
			return
		}
		if s.Role != panel.RoleAdmin {
			a.logger.Warn("ação de admin recusada", "username", s.Username, "role", s.Role, "path", r.URL.Path,
				"request_id", RequestID(r.Context()))
			a.audit(r, s, "acesso.negado", r.URL.Path, "perfil "+s.Role)
			b := a.baseFor(r.Context(), s, "Sem permissão", "frota")
			a.render(w, r, http.StatusForbidden, "proibido", b)
			return
		}
		next(w, r, s)
	}
}

func machinePath(id string) string { return "/painel/maquinas/" + url.PathEscape(id) }

// retireAction aposenta a máquina pelo painel, com o motivo e o usuário registrados.
func (a *api) retireAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("motivo"))
	if n := utf8.RuneCountInString(reason); n < minReasonLength || n > maxReasonLength {
		http.Redirect(w, r, machinePath(id)+"?erro=motivo", http.StatusSeeOther)
		return
	}
	changed, err := a.store.RetireMachine(r.Context(), id, reason, s.Username)
	if err != nil {
		a.internalError(w, r, "aposentar máquina", err)
		return
	}
	if changed {
		a.logger.Info("máquina aposentada pelo painel", "machine_id", id, "username", s.Username, "reason", reason,
			"request_id", RequestID(r.Context()))
		a.audit(r, s, "maquina.aposentar", id, reason)
	}
	http.Redirect(w, r, "/painel/aposentadas?aposentada=1", http.StatusSeeOther)
}

// restoreAction devolve a máquina à frota.
func (a *api) restoreAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	changed, err := a.store.RestoreMachine(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "restaurar máquina", err)
		return
	}
	if !changed {
		http.Redirect(w, r, "/painel/aposentadas", http.StatusSeeOther)
		return
	}
	a.logger.Info("máquina restaurada pelo painel", "machine_id", id, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "maquina.restaurar", id, "")
	http.Redirect(w, r, machinePath(id)+"?restaurada=1", http.StatusSeeOther)
}

// --- Modelos -----------------------------------------------------------------------

var pages = template.Must(template.New("").Parse(`
{{define "topo"}}<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.Brand}}</title>` + faviconLink + `
<style>` + themeCSS + `</style></head><body>
<input type="checkbox" id="menu" class="menu-chk" aria-label="recolher ou abrir o menu lateral">
<aside class="lado" aria-label="menu lateral">
<a class="marca" href="/painel/" title="{{.Brand}}">{{if .Logo}}<img src="{{.Logo}}" alt="" width="36" height="36">{{else}}<span class="escudo">` + shieldSVG + `</span>{{end}}<span class="rot">{{.Brand}}</span></a>
<div class="usuario" title="{{.Session.Greeting}} · {{.RoleLabel}}"><span class="avatar" aria-hidden="true">{{.Initials}}</span><span class="rot"><strong>{{.Session.Greeting}}</strong><small>{{.RoleLabel}} · {{.Session.SourceLabel}}</small></span></div>
<nav aria-label="principal">
<p class="grupo"><span class="rot">Navegação</span></p>
<a href="/painel/" title="Frota" {{if eq .Nav "frota"}}class="ativo" aria-current="page"{{end}}>` + ui.IcFrota + `<span class="rot">Frota</span></a>
<a href="/painel/inventario" title="Inventário" {{if eq .Nav "inventario"}}class="ativo" aria-current="page"{{end}}>` + ui.IcInventario + `<span class="rot">Inventário</span></a>
<a href="/painel/aposentadas" title="Aposentadas" {{if eq .Nav "aposentadas"}}class="ativo" aria-current="page"{{end}}>` + ui.IcAposentadas + `<span class="rot">Aposentadas</span></a>
<p class="grupo"><span class="rot">Patch</span></p>
<a href="/painel/patch?aba=geral" title="Patch › Visão geral" {{if and (eq .Nav "patch") (eq .Sub "geral")}}class="ativo" aria-current="page"{{end}}>` + ui.IcGeral + `<span class="rot">Visão geral</span>{{with .Badges.Sources}}<span class="cont" title="máquinas com a fonte de busca falhando">{{.}}</span>{{end}}</a>
<a href="/painel/patch" title="Patch › Sistema operacional" {{if and (eq .Nav "patch") (ne .Sub "geral") (ne .Sub "terceiros")}}class="ativo" aria-current="page"{{end}}>` + ui.IcSistema + `<span class="rot">Sistema</span></a>
<a href="/painel/patch?aba=terceiros" title="Patch › Programas de terceiros" {{if and (eq .Nav "patch") (eq .Sub "terceiros")}}class="ativo" aria-current="page"{{end}}>` + ui.IcTerceiros + `<span class="rot">Terceiros</span></a>
<p class="grupo"><span class="rot">Automação</span></p>
<a href="/painel/campanhas" title="Campanhas" {{if eq .Nav "campanhas"}}class="ativo" aria-current="page"{{end}}>` + ui.IcCampanhas + `<span class="rot">Campanhas</span>{{with .Badges.Paused}}<span class="cont" title="campanhas pausadas">{{.}}</span>{{end}}</a>
<a href="/painel/jobs" title="Jobs" {{if eq .Nav "jobs"}}class="ativo" aria-current="page"{{end}}>` + ui.IcJobs + `<span class="rot">Jobs</span></a>
<a href="/painel/aneis" title="Anéis e janelas" {{if eq .Nav "aneis"}}class="ativo" aria-current="page"{{end}}>` + ui.IcAneis + `<span class="rot">Anéis</span></a>
<a href="/painel/autoatendimento" title="Autoatendimento" {{if eq .Nav "autoatendimento"}}class="ativo" aria-current="page"{{end}}>` + ui.IcLoja + `<span class="rot">Autoatendimento</span>{{with .Badges.Requests}}<span class="cont" title="pedidos aguardando a TI">{{.}}</span>{{end}}</a>
{{if .Admin}}<p class="grupo"><span class="rot">Administração</span></p>
<a href="/painel/configuracao" title="Configuração" {{if and (eq .Nav "configuracao") (ne .Sub "auditoria")}}class="ativo" aria-current="page"{{end}}>` + ui.IcConfig + `<span class="rot">Configuração</span></a>
<a href="/painel/configuracao?aba=auditoria" title="Auditoria" {{if and (eq .Nav "configuracao") (eq .Sub "auditoria")}}class="ativo" aria-current="page"{{end}}>` + ui.IcAuditoria + `<span class="rot">Auditoria</span></a>
{{end}}
</nav></aside>
<label for="menu" class="veu" aria-hidden="true"></label>
<div class="area">
<header class="barra-topo">
<label for="menu" class="hamb" title="Recolher ou abrir o menu">` + ui.IcMenu + `<span class="sr">menu</span></label>
<span class="trilha">{{with .Section}}<span class="secao">{{.}}</span><span class="sep" aria-hidden="true">›</span>{{end}}<span class="titulo">{{.Title}}</span></span>
<span class="selos">{{if .ServerVersion}}<span class="selo" title="versão do servidor">Servidor <b>{{.ServerVersion}}</b></span>{{end}}{{if .AgentVersion}}<span class="selo" title="versão do agente publicada para as máquinas">Agente <b>{{.AgentVersion}}</b></span>{{end}}</span>
<details class="conta"><summary title="{{.Session.Username}}"><span class="avatar" aria-hidden="true">{{.Initials}}</span><span class="nome">{{.Session.Greeting}}</span>` + ui.IcAbaixo + `</summary>
<div class="conta-menu"><p><strong>{{.Session.Greeting}}</strong><span>{{.Session.Username}} · {{.Session.SourceLabel}} · {{.Session.Role}}</span></p>
<form method="post" action="/painel/sair"><button type="submit" class="sair">` + ui.IcSair + `Sair</button></form></div></details>
</header>
<main>
{{if .Flash}}<p class="aviso ok" role="status">{{.Flash}}</p>{{end}}
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{end}}
{{end}}

{{define "fim"}}</main></div></body></html>{{end}}

{{define "frota"}}{{template "topo" .}}
<h1>Frota</h1>
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="kpis">
<li><a class="kpi{{if not .Filter}} ativo{{end}}" href="/painel/"><span class="kpi-rot">Máquinas ativas</span><span class="kpi-val">{{.Total}}</span></a></li>
{{range .Counts}}<li><a class="kpi{{if .Active}} ativo{{end}}" href="/painel/?estado={{.State}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{.Color}}">{{.N}}</span></a></li>{{end}}
<li><div class="kpi"><span class="kpi-rot">Com exploração conhecida</span><span class="kpi-val v-vermelho">{{.Exploited}}</span><span class="kpi-sub">máquinas com CVE explorada pendente</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Ainda por HTTP</span><span class="kpi-val v-laranja">{{.HTTP}}</span><span class="kpi-sub">último check-in sem TLS</span></div></li>
</ul>
<div class="card"><div class="card-cab"><h2>Máquinas{{if .Filter}} · {{.FilterLabel}}{{end}}</h2>
<span><span class="muted">Avaliado em {{.EvaluatedAt}}</span> <span class="grupo-btn"><a class="btn" href="/painel/frota.csv{{if .Filter}}?estado={{.Filter}}{{end}}">CSV</a><a class="btn" href="/api/v1/compliance">JSON</a></span></span></div>
{{if .Rows}}{{if .Admin}}<form method="post" action="/painel/aneis/maquinas">{{if .Filter}}<input type="hidden" name="estado" value="{{.Filter}}">{{end}}{{end}}<div class="tabela"><table>
<thead><tr>{{if .Admin}}<th></th>{{end}}<th>Estado</th><th>Máquina</th><th>Nome</th><th>SO</th><th>Anel</th><th>Pendentes</th><th>Exploradas</th><th>Último contato</th></tr></thead>
<tbody>{{range .Rows}}<tr>
{{if $.Admin}}<td><input type="checkbox" name="maquina" value="{{.ID}}" aria-label="marcar {{.Short}}"></td>{{end}}
<td><span class="estado {{.State}}">{{.Label}}</span>{{if .Reasons}}<ul class="motivos">{{range .Reasons}}<li>{{.}}</li>{{end}}</ul>{{end}}</td>
<td><a href="/painel/maquinas/{{.ID}}"><code>{{.Short}}</code></a></td>
<td>{{.Name}}</td><td>{{.OS}}</td><td class="num">{{.Ring}}</td><td>{{.Pending}}</td>
<td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td><td>{{.Seen}}</td></tr>
{{end}}</tbody></table></div>
{{if .Admin}}<div class="lote"><label>Pôr as marcadas no anel <select name="anel">{{range .Rings}}<option value="{{.}}">{{.}}</option>{{end}}</select></label>
<button type="submit">Mover</button><span class="muted">0 = piloto, 1 = padrão. As janelas ficam em <a href="/painel/aneis">Anéis</a>.</span></div></form>{{end}}
{{else}}<p class="muted">Nenhuma máquina{{if .Filter}} neste estado{{end}}.</p>{{end}}
</div>
{{end}}
{{template "fim"}}{{end}}

{{define "inventario"}}{{template "topo" .}}
<h1>Inventário <span class="muted">› Equipamentos</span></h1>
<ul class="kpis">
<li><a class="kpi{{if not .Status}} ativo{{end}}" href="{{.KPIAll}}"><span class="kpi-rot">Equipamentos</span><span class="kpi-val">{{.Total}}</span></a></li>
<li><a class="kpi{{if eq .Status "online"}} ativo{{end}}" href="{{.KPIOnline}}"><span class="kpi-rot">Online</span><span class="kpi-val v-verde">{{.Online}}</span><span class="kpi-sub">check-in nas últimas 2 h</span></a></li>
<li><a class="kpi{{if eq .Status "offline"}} ativo{{end}}" href="{{.KPIOff}}"><span class="kpi-rot">Offline</span><span class="kpi-val v-laranja">{{.Offline}}</span><span class="kpi-sub">sem check-in há mais de 2 h</span></a></li>
<li><a class="kpi{{if eq .Status "sem-estado"}} ativo{{end}}" href="{{.KPINo}}"><span class="kpi-rot">Sem rede e discos</span><span class="kpi-val">{{.NoState}}</span><span class="kpi-sub">agente anterior à Aula 12.1</span></a></li>
</ul>
<div class="card"><div class="card-cab"><h2>Equipamentos</h2><span class="grupo-btn"><a class="btn" href="{{.CSV}}">CSV</a></span></div>
<form class="busca" method="get" action="/painel/inventario">
{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}
{{if ne .Order "nome"}}<input type="hidden" name="ordem" value="{{.Order}}">{{end}}{{if .Dir}}<input type="hidden" name="dir" value="{{.Dir}}">{{end}}
<select name="campo" aria-label="campo da busca">{{range .Fields}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Buscar" aria-label="texto da busca">
<label>Por página <select name="por">{{range .Pers}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="/painel/inventario">Limpar</a>{{end}}
</form>
{{if .Rows}}<div class="tabela"><table>
<thead><tr>{{range .Cols}}<th{{if .Sort}} aria-sort="{{.Sort}}"{{end}}><a href="{{.Href}}">{{.Label}}{{if .Arrow}} {{.Arrow}}{{end}}</a></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr>
<td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td>
<td>{{if .Online}}<span class="estado em_dia">online</span>{{else}}<span class="estado sem_dado_recente">offline</span>{{end}}</td>
<td>{{.Agent}}</td>
<td>{{.IP}}{{if .IPFromCheckin}} <span class="muted" title="O agente ainda não manda as interfaces: é o endereço de onde veio o check-in.">(check-in)</span>{{end}}</td>
<td{{if .UserAll}} title="{{.UserAll}}"{{end}}>{{.User}}{{if .UserMore}} <span class="muted">+{{.UserMore}}</span>{{end}}</td>
<td>{{.Collected}}</td><td>{{.Seen}}</td><td>{{.Channel}}</td><td>{{.OS}}</td><td>{{.Category}}</td><td><code>{{.Serial}}</code></td><td class="num">{{.Ring}}</td>
</tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>
{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}
{{range .Pages}}{{if .Gap}}<span>…</span>{{else if .Current}}<span class="atual" aria-current="page">{{.N}}</span>{{else}}<a href="{{.Href}}">{{.N}}</a>{{end}}{{end}}
{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum equipamento{{if .HasFilter}} com esse filtro{{end}}.</p>{{end}}
</div>
{{template "fim"}}{{end}}

{{define "patch-abas"}}<nav class="abas" aria-label="abas de Patch"><a href="/painel/patch?aba=geral"{{if eq . "geral"}} class="ativa" aria-current="page"{{end}}>Visão geral</a><a href="/painel/patch"{{if eq . "so"}} class="ativa" aria-current="page"{{end}}>Sistema operacional</a><a href="/painel/patch?aba=terceiros"{{if eq . "terceiros"}} class="ativa" aria-current="page"{{end}}>Programas de terceiros</a></nav>{{end}}

{{/* Automação (Aula 15.11): campanhas, jobs e anéis numa entrada só do menu, em abas. */}}
{{define "automacao-abas"}}<nav class="abas" aria-label="abas de Automação"><a href="/painel/campanhas"{{if eq . "campanhas"}} class="ativa" aria-current="page"{{end}}>Campanhas</a><a href="/painel/jobs"{{if eq . "jobs"}} class="ativa" aria-current="page"{{end}}>Jobs</a><a href="/painel/aneis"{{if eq . "aneis"}} class="ativa" aria-current="page"{{end}}>Anéis e janelas</a><a href="/painel/autoatendimento"{{if eq . "autoatendimento"}} class="ativa" aria-current="page"{{end}}>Autoatendimento</a></nav>{{end}}

{{define "configuracao"}}{{template "topo" .}}
<h1>Configuração</h1>
<nav class="abas" aria-label="abas da configuração"><a href="/painel/configuracao"{{if eq .Tab "sistema"}} class="ativa" aria-current="page"{{end}}>Sistema</a><a href="/painel/configuracao?aba=agente"{{if eq .Tab "agente"}} class="ativa" aria-current="page"{{end}}>Agente</a><a href="/painel/configuracao?aba=acesso"{{if eq .Tab "acesso"}} class="ativa" aria-current="page"{{end}}>Acesso</a><a href="/painel/configuracao?aba=auditoria"{{if eq .Tab "auditoria"}} class="ativa" aria-current="page"{{end}}>Auditoria</a><a href="/painel/configuracao?aba=autoatendimento"{{if eq .Tab "autoatendimento"}} class="ativa" aria-current="page"{{end}}>Autoatendimento</a></nav>
{{if eq .Tab "autoatendimento"}}{{with .SelfService}}
{{if .NoManifest}}<p class="aviso atencao">O servidor está sem as regras de terceiros: o autoatendimento depende delas.</p>{{else}}
<div class="card"><div class="card-cab"><h2>O que o usuário pode pedir</h2></div>
<p class="muted">Os pedidos feitos e as recusas ficam em <a href="/painel/autoatendimento">Automação › Autoatendimento</a>. Por programa com instalação pelo patchd. Atualizar: quem já tem o programa leva para a versão aprovada mais nova. Instalar: quem não tem instala essa versão. Grupos do AD pelo sAMAccountName (GG-Exemplo ou DOMINIO\GG-Exemplo), separados por vírgula; vazio: qualquer usuário da máquina. O agente confere pelos grupos do token do Windows de quem pede, inclusive os aninhados. A versão vem sempre da aprovação em Patch: a oferta não aprova nada. Aprovação da TI (Aula 18.3): cada pedido espera alguém aprovar em Automação › Autoatendimento antes de virar job. Desmarcar atualizar e instalar tira o programa da loja.</p>
<style>.ss-lote{display:flex;flex-wrap:wrap;gap:.6rem 1rem;align-items:center;border:1px solid var(--borda);border-radius:var(--raio);padding:.7rem .9rem;margin:.9rem 0 0}.ss-lote legend{font-weight:600;padding:0 .3rem}.ss-lote label{display:flex;gap:.35rem;align-items:center}.ss-lote input[type=text]{width:16rem}tr.ss-erro td{background:#fdf2f2}.ss-erro-msg{color:var(--vermelho);font-size:.85rem;margin-top:.25rem}.ss-salvar{display:flex;gap:.6rem;align-items:center;margin:.9rem 0 0}</style>
<form method="post" action="/painel/configuracao/autoatendimento">
<div class="tabela"><table><thead><tr><th aria-label="marcar para o lote"></th><th>Programa</th><th>Aprovada mais nova</th><th>Atualizar</th><th>Instalar</th><th>Aprovação da TI</th><th>Grupos do AD</th><th>Última mudança</th></tr></thead>
<tbody>{{range .Rows}}<tr{{if .Problem}} class="ss-erro"{{end}}><td><input type="checkbox" name="sel" value="{{.AppID}}" aria-label="marcar {{.Name}} para o lote" {{if .Selected}}checked{{end}}><input type="hidden" name="apps" value="{{.AppID}}"></td>
<td>{{.Name}} <span class="muted"><code>{{.AppID}}</code></span></td><td><code>{{.Approved}}</code></td>
<td><input type="checkbox" name="atualizar.{{.AppID}}" value="1" aria-label="atualizar {{.Name}}" {{if .CanUpdate}}checked{{end}}></td>
<td><input type="checkbox" name="instalar.{{.AppID}}" value="1" aria-label="instalar {{.Name}}" {{if .CanInstall}}checked{{end}}></td>
<td><input type="checkbox" name="aprovacao.{{.AppID}}" value="1" aria-label="aprovação da TI para {{.Name}}" {{if .NeedsApproval}}checked{{end}}></td>
<td><input type="text" name="grupos.{{.AppID}}" maxlength="2000" value="{{.Groups}}" placeholder="qualquer usuário" aria-label="grupos de {{.Name}}">{{if .Problem}}<div class="ss-erro-msg">{{.Problem}}</div>{{end}}</td>
<td class="muted">{{if .Active}}{{.Updated}}{{else}}fora da loja{{end}}</td></tr>{{end}}</tbody></table></div>
<div class="ss-salvar"><button type="submit" name="modo" value="salvar">Salvar alterações</button><span class="muted">grava só as linhas que mudaram</span></div>
<fieldset class="ss-lote"><legend>Em lote</legend>
<label>Aplicar a <select name="lote_alvo"><option value="marcados">os marcados</option><option value="todos">todos os programas</option></select></label>
<label>Atualizar <select name="lote_atualizar"><option value="">manter</option><option value="1">sim</option><option value="0">não</option></select></label>
<label>Instalar <select name="lote_instalar"><option value="">manter</option><option value="1">sim</option><option value="0">não</option></select></label>
<label>Aprovação da TI <select name="lote_aprovacao"><option value="">manter</option><option value="1">sim</option><option value="0">não</option></select></label>
<label>Grupos <select name="lote_grupos_modo"><option value="">manter</option><option value="trocar">trocar por</option></select></label><input type="text" name="lote_grupos" maxlength="2000" placeholder="vazio: qualquer usuário" aria-label="grupos para o lote">
<button class="sec" type="submit" name="modo" value="lote">Aplicar</button>
<span class="muted">vale por cima do que está na tela; o que não estiver em "manter" substitui</span></fieldset>
</form></div>
<div class="card"><div class="card-cab"><h2>Prévia da loja</h2></div>
<p class="muted">O que um usuário destes grupos veria nesta máquina agora, com o último inventário dela. No app do usuário (Aula 17.4), os grupos vêm do AD.</p>
<form class="busca" method="get" action="/painel/configuracao"><input type="hidden" name="aba" value="autoatendimento">
<select name="maquina" aria-label="máquina"><option value="">Máquina Windows…</option>{{range .Machines}}<option value="{{.ID}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="text" name="grupos" maxlength="2000" value="{{.Groups}}" placeholder="grupos do usuário, separados por vírgula" aria-label="grupos do usuário">
<button type="submit">Ver a loja</button></form>
{{if .Note}}<p class="aviso atencao">{{.Note}}</p>{{end}}
{{if .Ran}}{{if .Items}}<div class="tabela"><table><thead><tr><th>Programa</th><th>Instalado</th><th>Ação</th><th>Versão</th><th>Observação</th></tr></thead>
<tbody>{{range .Items}}<tr><td>{{.Name}}</td><td>{{if .Installed}}<code>{{.Installed}}</code>{{else}}—{{end}}</td><td>{{.Label}}</td><td>{{if .Target}}<code>{{.Target}}</code>{{else}}—{{end}}</td><td class="muted">{{.Reason}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">A loja está vazia para estes grupos nesta máquina.</p>{{end}}{{end}}
</div>{{end}}
{{end}}{{end}}
{{if eq .Tab "auditoria"}}{{with .Audit}}
<div class="card"><div class="card-cab"><h2>Trilha de auditoria</h2><span class="grupo-btn"><a class="btn" href="{{.CSV}}">CSV</a></span></div>
<p class="muted">Logins, ações pelo painel, pedidos do autoatendimento, comandos no terminal (origem "linha de comando") e exportações, do mais novo para o mais antigo. Só se acrescenta: nada aqui se edita; os eventos além do prazo da retenção saem, e a limpeza fica registrada aqui. O request_id liga cada evento à linha do log do servidor.</p>
<form class="busca" method="get" action="/painel/configuracao"><input type="hidden" name="aba" value="auditoria">
<input type="search" name="usuario" value="{{.Q.User}}" maxlength="64" placeholder="Usuário (exato)" aria-label="usuário">
<select name="acao" aria-label="ação"><option value="">Todas as ações</option><optgroup label="Grupos">{{range .Groups}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup><optgroup label="Ações">{{range .Actions}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup></select>
<select name="periodo" aria-label="período">{{range .Periods}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<button type="submit">Filtrar</button></form>
{{if .Rows}}<div class="tabela"><table><thead><tr><th>Quando</th><th>Usuário</th><th>Origem</th><th>Ação</th><th>Alvo</th><th>Detalhe</th><th>De onde</th><th>Request ID</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="nowrap">{{.When}}</td><td><code>{{.User}}</code></td><td>{{.Source}}</td><td>{{if .Denied}}<span class="expl">{{.Action}}</span>{{else}}{{.Action}}{{end}}</td><td><code>{{.Target}}</code></td><td>{{.Detail}}</td><td><code>{{.Remote}}</code></td><td><code>{{.RequestID}}</code></td></tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas">{{if .First}}<a href="{{.First}}">« mais novos</a>{{end}}{{if .Next}}<a href="{{.Next}}">mais antigos ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum evento com esse filtro.</p>{{end}}</div>
{{end}}{{end}}
{{if eq .Tab "sistema"}}<div class="card"><div class="card-cab"><h2>Servidor</h2><span class="muted">só leitura: a configuração fica no ambiente (.env) e nos arquivos de segredo</span></div>
<dl class="dados">{{range .System}}<dt>{{.Label}}</dt><dd{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Value}}</dd>{{end}}</dl>
{{if .NotifyOn}}<form class="lote" method="post" action="/painel/configuracao/notificacoes/teste"><button class="sec" type="submit">Mandar e-mail de teste</button> <span class="muted">uma mensagem para a lista de PATCHD_NOTIFY_TO; fica na auditoria</span></form>{{end}}</div>
{{with .Appearance}}<div class="card"><div class="card-cab"><h2>Aparência</h2><span class="muted">o logotipo do menu lateral e da tela de login</span></div>
<div class="logo-linha"><div class="logo-previa">{{if .URL}}<img src="{{.URL}}" alt="logotipo atual">{{else}}<span class="escudo">` + shieldSVG + `</span>{{end}}</div>
<div class="logo-acoes">{{if .URL}}<p>{{.Type}} · {{.KB}} KB · enviado por {{.UpdatedBy}} em {{.UpdatedAt}}</p>{{else}}<p class="muted">Sem logotipo: o painel usa o ícone padrão.</p>{{end}}
<form class="acao" method="post" enctype="multipart/form-data" action="/painel/configuracao/aparencia/logo"><input type="hidden" name="op" value="enviar"><input type="file" name="arquivo" accept="image/png,image/jpeg" required aria-label="arquivo do logotipo"><button type="submit">{{if .URL}}Trocar{{else}}Enviar{{end}}</button></form>
{{if .URL}}<form class="acao" method="post" enctype="multipart/form-data" action="/painel/configuracao/aparencia/logo"><input type="hidden" name="op" value="remover"><button class="perigo" type="submit">Remover o logotipo</button></form>{{end}}
<p class="muted nota">PNG ou JPEG, até {{.MaxKB}} KB e {{.MaxSide}}×{{.MaxSide}} pixels. Quadrado (ou quase) e com fundo transparente fica melhor sobre o azul da barra. O servidor confere o conteúdo, não o nome do arquivo, e a troca fica na auditoria.</p></div></div></div>{{end}}{{end}}
{{if eq .Tab "agente"}}<div class="card"><div class="card-cab"><h2>Executáveis{{if .Version}} da versão {{.Version}}{{end}}</h2><span class="muted">a versão publicada, a mesma que os agentes recebem na atualização</span></div>
{{if .Files}}<div class="tabela"><table><thead><tr><th>Sistema</th><th>Arquitetura</th><th>Arquivo</th><th>Tamanho</th><th>SHA-256</th></tr></thead>
<tbody>{{range .Files}}<tr><td>{{.OS}}</td><td>{{.Arch}}</td><td><a href="{{.Href}}"><code>{{.Name}}</code></a></td><td class="num">{{.Size}}</td><td><code>{{.SHA}}…</code></td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">{{.FilesNote}}</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>CA do servidor</h2></div>
{{if .CA}}<p><a class="btn" href="/painel/configuracao/agente/ca.crt">Baixar ca.crt</a> <span class="muted">SHA-256 <code>{{.CASHA}}…</code>. As máquinas confiam só nela para falar com o servidor (-ca-file).</span></p>
{{else}}<p class="muted">Sem PATCHD_AGENT_CA_FILE: o comando de instalação sai sem -ca-file e o agente usa as CAs do sistema. Para uma CA interna, aponte PATCHD_AGENT_CA_FILE para o PEM dela.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Tokens de registro</h2><span class="muted">os mesmos do patchd-server token</span></div>
<form class="grade" method="post" action="/painel/configuracao/agente/token">
<label for="nota">Descrição</label><input id="nota" name="nota" maxlength="{{.MaxNote}}" placeholder="ex.: laboratório da SEAT, outubro">
<label for="validade">Validade</label><select id="validade" name="validade">{{range .TTLs}}<option value="{{.Hours}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="usos">Máquinas</label><input id="usos" name="usos" type="number" min="1" max="{{.MaxUses}}" value="1">
<span></span><span><button type="submit">Criar token</button> <span class="muted">O token aparece uma única vez, com o comando de instalação. Prazo curto e poucos usos limitam o estrago de um vazamento.</span></span>
</form>
{{if .Tokens}}<div class="tabela"><table><thead><tr><th>ID</th><th>Descrição</th><th>Criado</th><th>Vence</th><th>Usos</th><th>Estado</th><th></th></tr></thead>
<tbody>{{range .Tokens}}<tr><td class="num">{{.ID}}</td><td>{{.Note}}</td><td>{{.Created}}</td><td>{{.Expires}}</td><td class="num">{{.Uses}} de {{.MaxUses}}</td><td>{{.Status}}</td>
<td>{{if .CanRevoke}}<form class="inline" method="post" action="/painel/configuracao/agente/token/{{.ID}}/revogar"><button class="perigo" type="submit">Revogar</button></form>{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum token criado.</p>{{end}}</div>{{end}}
{{if eq .Tab "acesso"}}{{with .Access}}
<div class="card"><div class="card-cab"><h2>Sessões abertas</h2><span class="muted">tirar alguém de um grupo do AD ou do RHBK não derruba a sessão que já está aberta: encerre aqui</span></div>
{{if .Sessions}}<div class="tabela"><table><thead><tr><th>Usuário</th><th>Origem</th><th>Perfil</th><th>Entrou</th><th>Último uso</th><th>Vence</th><th></th></tr></thead>
<tbody>{{range .Sessions}}<tr><td>{{if .Name}}{{.Name}} · {{end}}<code>{{.User}}</code>{{if .Current}} <span class="muted">(esta sessão)</span>{{end}}</td><td>{{.Source}}</td><td>{{.Role}}</td><td>{{.Created}}</td><td>{{.Seen}}</td><td>{{.Expires}}</td>
<td>{{if not .Current}}<form class="inline" method="post" action="/painel/configuracao/acesso/sessoes/{{.ID}}/encerrar"><button class="perigo" type="submit">Encerrar</button></form>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if .SessUsers}}<form class="lote" method="post" action="/painel/configuracao/acesso/sessoes/usuario"><label>Encerrar todas as sessões de <select name="usuario">{{range .SessUsers}}<option value="{{.User}}">{{if .Name}}{{.Name}} ({{.User}}){{else}}{{.User}}{{end}}</option>{{end}}</select></label><button class="perigo" type="submit">Encerrar todas</button></form>{{end}}
{{else}}<p class="muted">Nenhuma sessão aberta.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Login pelo AD (LDAPS)</h2><span class="muted">{{if .AD}}ligado{{else}}desligado{{end}} · os valores ficam só no .env (não aparecem aqui nem no log)</span></div>
{{if .AD}}<dl class="dados">{{range .AD.Vars}}<dt><code>{{.Name}}</code></dt><dd>{{if .Set}}definida{{else}}vazia{{end}}{{if .Note}} <span class="muted">· {{.Note}}</span>{{end}}</dd>{{end}}</dl>
<h3>Testar um login</h3>
<form class="grade" method="post" action="/painel/configuracao/acesso/testar-ad" autocomplete="off">
<label for="usuario">Usuário</label><input id="usuario" name="usuario" required maxlength="64" placeholder="usuário da rede, sem o domínio" autocomplete="off">
<label for="senha">Senha</label><input id="senha" name="senha" type="password" required maxlength="1024" autocomplete="new-password">
<span></span><span><button type="submit">Testar</button> <span class="muted">Confere a conexão LDAPS, a senha e os grupos, como o patchd-server ad check. Não abre sessão; a senha não vai para o log.</span></span>
</form>
{{with .ADResult}}<h3>Resultado para {{.User}}</h3><dl class="dados">{{range .Lines}}<dt>{{.Label}}</dt><dd{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Value}}</dd>{{end}}</dl>{{end}}
{{else}}<p class="muted">Sem PATCHD_LDAP_HOST. Para ligar, defina as PATCHD_LDAP_* no .env e reinicie o servidor.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Login pelo RHBK (OIDC)</h2><span class="muted">{{if .OIDC}}ligado{{else}}desligado{{end}}</span></div>
{{with .OIDC}}<dl class="dados"><dt>Emissor</dt><dd><code>{{.Issuer}}</code></dd><dt>Cliente</dt><dd><code>{{.ClientID}}</code> <span class="muted">· o segredo vem de arquivo</span></dd>
<dt>Claim dos grupos</dt><dd><code>{{.Claim}}</code></dd><dt>Papel admin</dt><dd><code>{{.AdminRole}}</code></dd><dt>Papel leitura</dt><dd><code>{{.ReadRole}}</code></dd></dl>
<form class="acao" method="post" action="/painel/configuracao/acesso/testar-oidc"><button class="sec" type="submit">Testar a descoberta</button></form>{{end}}
{{if .OIDCResult}}<p class="aviso {{if .OIDCOK}}ok{{else}}erro{{end}}">{{.OIDCResult}}</p>{{end}}
{{if not .OIDC}}<p class="muted">Sem PATCHD_OIDC_ISSUER. Para ligar, defina as PATCHD_OIDC_* no .env (o segredo do cliente por arquivo) e reinicie o servidor.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Usuários locais</h2><span class="muted">{{.Local}}</span></div>
{{if .Users}}<div class="tabela"><table><thead><tr><th>Usuário</th><th>Perfil</th><th>Criado</th><th></th></tr></thead>
<tbody>{{range .Users}}<tr><td><code>{{.Name}}</code>{{if .Self}} <span class="muted">(você)</span>{{end}}</td><td>{{.Role}}</td><td>{{.Created}}</td>
<td>{{if not .Self}}<form class="inline" method="post" action="/painel/configuracao/acesso/usuarios/{{.Name}}/senha"><button class="sec" type="submit">Nova senha</button></form>
{{if not .Last}}<form class="inline" method="post" action="/painel/configuracao/acesso/usuarios/{{.Name}}/remover"><button class="perigo" type="submit">Remover</button></form>{{else}}<span class="muted">único admin local</span>{{end}}{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="aviso atencao">Nenhum usuário local: {{if .AD}}se o AD cair, ninguém entra no painel{{else}}sem AD, ninguém entra pelo formulário de senha{{end}}. Crie ao menos um admin local.</p>{{end}}
<h3>Novo usuário local</h3>
<form class="grade" method="post" action="/painel/configuracao/acesso/usuarios">
<label for="nome">Nome</label><input id="nome" name="nome" required maxlength="64" placeholder="ex.: contingencia.seat">
<span>Perfil</span><span><label><input type="radio" name="perfil" value="admin"> admin</label> <label><input type="radio" name="perfil" value="leitura" checked> leitura</label></span>
<span></span><span><button type="submit">Criar</button> <span class="muted">A senha é gerada pelo servidor e aparece uma única vez.</span></span>
</form></div>
{{end}}{{end}}
{{template "fim"}}{{end}}

{{define "senha-local"}}{{template "topo" .}}
<p><a href="/painel/configuracao?aba=acesso">← Configuração › Acesso</a></p>
<h1>{{if .Created}}Usuário local criado{{else}}Nova senha{{end}}: {{.User}}</h1>
<p class="aviso atencao">Guarde agora: a senha não será mostrada de novo.{{if not .Created}} As sessões locais desse usuário foram encerradas.{{end}} Perfil: {{.Role}}.</p>
<div class="card"><div class="card-cab"><h2>Senha</h2></div><pre class="comando">{{.Password}}</pre></div>
<p class="muted">Entregue a senha por um canal separado do nome do usuário. Para trocar de novo, use "Nova senha" em Configuração › Acesso ou o patchd-server user password.</p>
{{template "fim"}}{{end}}

{{define "token-criado"}}{{template "topo" .}}
<p><a href="/painel/configuracao?aba=agente">← Configuração › Agente</a></p>
<h1>Token de registro {{.ID}}</h1>
<p class="aviso atencao">Guarde agora: o token não será mostrado de novo. Vale até {{.Expires}}, para {{.Uses}} máquina(s){{if .Note}} ({{.Note}}){{end}}.</p>
<div class="card"><div class="card-cab"><h2>Token</h2></div><pre class="comando">{{.Token}}</pre></div>
<div class="card"><div class="card-cab"><h2>Windows</h2><span class="muted">{{if .Version}}patchd-agent {{.Version}} · {{end}}baixe o executável{{if .CA}} e o ca.crt{{end}} em Configuração › Agente</span></div><pre class="comando">{{.WinCommand}}</pre></div>
<div class="card"><div class="card-cab"><h2>Linux</h2></div><pre class="comando">{{.LinCommand}}</pre></div>
<p class="muted">O token vai num arquivo temporário apagado em seguida: na linha de comando ele ficaria no histórico e na lista de processos. O install é repetível: rodar de novo na mesma máquina não gasta outro uso.</p>
{{template "fim"}}{{end}}

{{define "patch-geral"}}{{template "topo" .}}
<h1>Patch <span class="muted">› Visão geral</span></h1>
{{template "patch-abas" "geral"}}
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="kpis">{{range .KPIs}}<li><a class="kpi" href="{{.Href}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{.Color}}">{{.Value}}</span><span class="kpi-sub">{{.Sub}}</span></a></li>{{end}}</ul>
<div class="card"><div class="card-cab"><h2>Precisa de atenção</h2><span class="muted">avaliado em {{.EvaluatedAt}}</span></div>
{{if .Attention}}<ul class="atencao-lista">{{range .Attention}}<li class="{{.Level}}"><a href="{{.Href}}">{{.Text}}</a></li>{{end}}</ul>
{{else}}<p class="muted">Nada pede atenção agora.</p>{{end}}</div>
<div class="duas-col">
<div class="card"><div class="card-cab"><h2>Por anel</h2><a class="muted" href="/painel/aneis">Anéis</a></div>
{{if .Rings}}<div class="tabela"><table><thead><tr><th>Anel</th><th>Máquinas</th><th>Em dia</th><th>Faltando</th><th>Reinício</th><th>Sem contato</th><th>Outros</th><th>% em dia</th></tr></thead>
<tbody>{{range .Rings}}<tr><td>{{.Ring}}</td><td class="num">{{.Machines}}</td><td class="num">{{.OK}}</td><td class="num">{{.Missing}}</td><td class="num">{{.Reboot}}</td><td class="num">{{.Stale}}</td><td class="num">{{.NA}}</td>
<td class="nowrap"><span class="barra" title="{{.Pct}}% em dia"><span style="width: {{.Pct}}%"></span></span> <span class="muted">{{.Pct}}%</span></td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma máquina avaliada.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Por sistema</h2></div>
{{if .Systems}}<div class="tabela"><table><thead><tr><th>Sistema</th><th>Máquinas</th><th>Em dia</th><th>Pendentes</th><th>Exploradas</th><th>% em dia</th></tr></thead>
<tbody>{{range .Systems}}<tr><td>{{.OS}}</td><td class="num">{{.Machines}}</td><td class="num">{{.OK}}</td><td class="num">{{.Pending}}</td><td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td>
<td class="nowrap"><span class="barra" title="{{.Pct}}% em dia"><span style="width: {{.Pct}}%"></span></span> <span class="muted">{{.Pct}}%</span></td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma máquina avaliada.</p>{{end}}</div>
</div>
<div class="card"><div class="card-cab"><h2>Correções aguardando aprovação</h2><a class="muted" href="{{.FixesHref}}">todos os itens aguardando</a></div>
{{if .Fixes}}<p class="muted">No Windows e no macOS, uma atualização (build ou KB) fecha muitas CVEs de uma vez: aprove pela correção, depois de conferir o resumo. Pacotes do Linux seguem aprovados item a item ou em lote.</p>
<div class="tabela"><table><thead><tr><th>Correção</th><th>Tipo</th><th>Sistemas</th><th>Itens</th><th>Exploradas</th><th>Máquinas</th><th></th></tr></thead>
<tbody>{{range .Fixes}}<tr><td>{{if .FixedIn}}<code>{{.FixedIn}}</code>{{else}}<span class="muted">sem correção indicada</span>{{end}}</td><td>{{.Kind}}</td><td>{{.Systems}}</td><td class="num">{{.Items}}</td>
<td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td><td class="num">{{.Machines}}</td><td class="nowrap"><a href="{{.Href}}">{{if and $.Admin .Whole}}Ver{{else}}Ver e aprovar{{end}}</a>{{if and $.Admin .Whole}} <form class="inline" method="post" action="/painel/patch/correcao"><input type="hidden" name="fix" value="{{.FixedIn}}"><button class="sec" type="submit">Aprovar a correção</button></form>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if .MoreFixes}}<p class="muted">E mais {{.MoreFixes}} correção(ões): <a href="{{.FixesHref}}">ver todos os itens aguardando</a>.</p>{{end}}
{{else}}<p class="muted">Nenhuma correção aguardando aprovação.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Execução nos últimos 7 dias</h2><a class="muted" href="/painel/jobs">Jobs</a></div>
{{if .Exec}}<div class="tabela"><table><thead><tr><th>Tipo</th><th>Criados</th><th>Concluídos</th><th>Sem sucesso</th><th>Adiados</th><th>Em aberto</th></tr></thead>
<tbody>{{range .Exec}}<tr><td>{{.Type}}</td><td class="num">{{.Created}}</td><td class="num">{{.Done}}</td><td class="num">{{.Failed}}</td><td class="num">{{.Postponed}}</td><td class="num">{{.Open}}</td></tr>{{end}}
<tr class="total"><td>Total</td><td class="num">{{.ExecTotal.Created}}</td><td class="num">{{.ExecTotal.Done}}</td><td class="num">{{.ExecTotal.Failed}}</td><td class="num">{{.ExecTotal.Postponed}}</td><td class="num">{{.ExecTotal.Open}}</td></tr></tbody></table></div>
{{else}}<p class="muted">Nenhum job nos últimos 7 dias.</p>{{end}}</div>
{{end}}
{{template "fim"}}{{end}}

{{define "patch-terceiros"}}{{template "topo" .}}
<h1>Patch <span class="muted">› Programas de terceiros</span></h1>
{{template "patch-abas" "terceiros"}}
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="kpis">
<li><div class="kpi"><span class="kpi-rot">Programas com problema</span><span class="kpi-val">{{.Programs}}</span><span class="kpi-sub">em {{.Hosts}} sistemas</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Instalações desatualizadas</span><span class="kpi-val v-laranja">{{.Outdated}}</span><span class="kpi-sub">abaixo da referência ou do mínimo</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Instalações fora de suporte</span><span class="kpi-val v-vermelho">{{.EOL}}</span><span class="kpi-sub">sem correção do fabricante</span></div></li>
</ul>
<div class="card"><div class="card-cab"><h2>Programas{{if not .All}} com problema{{end}}</h2><span class="muted">{{if .All}}<a href="/painel/patch?aba=terceiros">só os com problema</a>{{else}}<a href="/painel/patch?aba=terceiros&amp;todos=1">mostrar todos os identificados</a>{{end}}</span></div>
<p class="muted">O patchd não instala programas de terceiros: a lista diz o que distribuir e onde (a distribuição continua pelo ITOM). A referência vem do catálogo de terceiros (winget e feeds dos fabricantes, Aula 6.5).</p>
{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>Programa</th><th>Referência</th><th>Instalações</th><th>Desatualizadas</th><th>Fora de suporte</th><th>Versões instaladas</th><th>Origem</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><a href="{{.Href}}">{{.Program}}</a></td><td><code>{{.Reference}}</code></td><td class="num">{{.Installs}}</td>
<td class="num">{{if .Outdated}}<span class="v-laranja">{{.Outdated}}</span>{{else}}0{{end}}</td><td class="num">{{if .EOL}}<span class="expl">{{.EOL}}</span>{{else}}0{{end}}</td>
<td><code>{{.Versions}}</code></td><td class="muted">{{.Origin}}</td></tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum programa de terceiros {{if .All}}identificado{{else}}com problema{{end}} na frota.</p>{{end}}
</div>{{end}}
{{template "fim"}}{{end}}

{{define "patch-programa"}}{{template "topo" .}}
<p><a href="/painel/patch?aba=terceiros">← Programas de terceiros</a></p>
<h1>{{.P.Program}}</h1>
<div class="card"><dl class="dados">
<dt>Referência</dt><dd>{{if .P.Reference}}<code>{{.P.Reference}}</code>{{else}}—{{end}}</dd>
{{if .P.Origin}}<dt>Origem</dt><dd>{{.P.Origin}}</dd>{{end}}
<dt>Instalações</dt><dd>{{len .P.Installs}} ({{.P.Outdated}} desatualizadas, {{.P.EOL}} fora de suporte)</dd>
</dl></div>
{{if .Install}}<div class="card"><div class="card-cab"><h2>Instaladores guardados</h2><span class="muted">winget <code>{{.Install.Winget}}</code>{{if .Install.Type}} · tipo {{.Install.Type}}{{end}} · assinatura {{if .Install.Signer}}exigida: {{.Install.Signer}}{{else}}não exigida{{end}}{{if .Install.Processes}} · não instala com {{.Install.Processes}} aberto{{end}}</span></div>
{{if .Versions}}<div class="tabela"><table>
<thead><tr><th>Versão</th><th>Instaladores</th><th>Aprovação</th></tr></thead>
<tbody>{{range .Versions}}<tr><td><code>{{.Version}}</code></td>
<td>{{range .Packages}}<div>{{.Type}} · {{.Arch}} · {{.Size}} · <code>{{.SHA}}</code> <span class="muted">{{.Fetched}}</span></div>{{end}}</td>
<td>{{if .Approved}}<span class="estado em_dia">aprovado</span> <span class="muted">{{.ApprovedBy}} · {{.ApprovedAt}}</span>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}{{if .CampaignHref}} <a class="btn" href="{{.CampaignHref}}">Campanha por anel</a>{{end}}{{if .CanRevoke}}<form class="inline" method="post" action="/painel/patch/terceiros/revogar"><input type="hidden" name="app" value="{{$.P.AppID}}"><input type="hidden" name="version" value="{{.Version}}"><button class="perigo" type="submit">Revogar</button></form>{{end}}
{{else}}<span class="estado reboot_pendente">aguardando</span>{{if .CanApprove}}<form class="acao" method="post" action="/painel/patch/terceiros/aprovar"><input type="hidden" name="app" value="{{$.P.AppID}}"><input type="hidden" name="version" value="{{.Version}}"><input name="nota" maxlength="{{$.MaxNote}}" placeholder="Observação (opcional), ex.: testado no anel piloto"><button type="submit">Aprovar</button></form>{{end}}{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum instalador guardado. No servidor: <code>patchd-server package fetch -app {{.P.AppID}}</code>.</p>{{end}}
{{if not .Admin}}<p class="muted">Aprovar exige o perfil admin.</p>{{end}}
</div>{{end}}
<div class="card"><div class="card-cab"><h2>Instalações</h2></div><div class="tabela"><table>
<thead><tr><th>Máquina</th><th>Como aparece no inventário</th><th>Instalada</th><th>Situação</th><th>Escopo</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><a href="/painel/maquinas/{{.MachineID}}?aba=pendencias">{{if .Machine}}{{.Machine}}{{else}}<code>{{.MachineID}}</code>{{end}}</a></td>
<td>{{.Name}}</td><td><code>{{.Installed}}</code></td><td><span class="estado {{.Class}}">{{.Label}}</span></td><td>{{.ScopeLabel}}</td></tr>{{end}}</tbody></table></div></div>
{{template "fim"}}{{end}}

{{define "patch"}}{{template "topo" .}}
<h1>Patch <span class="muted">› Sistema operacional</span></h1>
{{template "patch-abas" "so"}}
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="kpis">
<li><a class="kpi{{if not .Status}} ativo{{end}}" href="{{.KPIAll}}"><span class="kpi-rot">Itens pendentes</span><span class="kpi-val">{{.Total}}</span><span class="kpi-sub">em {{.Hosts}} sistemas</span></a></li>
<li><a class="kpi{{if eq .Status "exploradas"}} ativo{{end}}" href="{{.KPIExpl}}"><span class="kpi-rot">Com exploração conhecida</span><span class="kpi-val v-vermelho">{{.Exploited}}</span></a></li>
<li><a class="kpi{{if eq .Status "aguardando"}} ativo{{end}}" href="{{.KPIWait}}"><span class="kpi-rot">Aguardando aprovação</span><span class="kpi-val v-laranja">{{.Waiting}}</span></a></li>
<li><a class="kpi{{if eq .Status "aprovados"}} ativo{{end}}" href="{{.KPIApproved}}"><span class="kpi-rot">Aprovados</span><span class="kpi-val v-verde">{{.Approved}}</span></a></li>
</ul>
<div class="card"><div class="card-cab"><h2>Itens pendentes</h2><span class="muted">avaliação de agora, contra o catálogo</span></div>
<form class="busca" method="get" action="/painel/patch">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Item, correção ou aviso (curl, CVE-2026-…, KB…)" aria-label="buscar item">
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="/painel/patch">Limpar</a>{{end}}</form>
{{if .WholeFix}}<form class="aviso atencao" method="post" action="/painel/patch/correcao"><input type="hidden" name="fix" value="{{.WholeFix}}">Os {{.WholeFixN}} itens aguardando deste filtro são CVEs da mesma correção, <code>{{.WholeFix}}</code>. <button type="submit">Conferir a correção inteira</button></form>{{end}}
{{if .Rows}}{{if .CanBatch}}<form method="post" action="/painel/patch/aprovar-lote">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Q}}<input type="hidden" name="q" value="{{.Q}}">{{end}}{{if gt .Page 1}}<input type="hidden" name="pagina" value="{{.Page}}">{{end}}{{end}}
<div class="tabela"><table>
<thead><tr>{{if .CanBatch}}<th class="col-lote">Lote</th>{{end}}<th>Item</th><th>Tipo</th><th>Corrigido em</th><th>Severidade</th><th>Explorada</th><th>Sistemas</th><th>Catálogo</th><th>Aprovação</th></tr></thead>
<tbody>{{range .Rows}}<tr>{{if $.CanBatch}}<td class="col-lote">{{if .Sel}}<input type="checkbox" name="sel" value="{{.Sel}}" aria-label="marcar {{.ID}}">{{end}}</td>{{end}}<td><a href="{{.Href}}"><code>{{.ID}}</code></a>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}</td>
<td>{{.Kind}}</td><td><code>{{.FixedIn}}</code></td><td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td>
<td class="num"><a href="{{.Href}}">{{.Machines}}</a></td><td>{{.Catalogs}}</td>
<td>{{if .ApprovedBy}}<span class="estado em_dia">aprovado</span> <span class="muted">{{.ApprovedBy}} · {{.ApprovedAt}}</span>{{else}}<span class="estado reboot_pendente">aguardando</span>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if .CanBatch}}<div class="lote"><button type="submit">Conferir marcados</button>
<button class="sec" type="submit" name="filtro" value="1">Conferir todos os {{.FilterWaiting}} aguardando{{if .HasFilter}} deste filtro{{end}}</button>
<span class="muted">Nada é aprovado antes da conferência.{{if gt .FilterWaiting .MaxBatch}} Um lote leva até {{.MaxBatch}} itens.{{end}}</span></div></form>{{end}}
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum item{{if .HasFilter}} com esse filtro{{else}} pendente na frota{{end}}.</p>{{end}}
</div>{{end}}
{{template "fim"}}{{end}}

{{define "patch-lote"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Patch</a></p>
<h1>Aprovar em lote <span class="muted">› conferência</span></h1>
<p class="aviso atencao">{{len .Rows}} {{if eq (len .Rows) 1}}item será aprovado{{else}}itens serão aprovados{{end}} com a correção indicada{{if .Exploited}}, {{.Exploited}} com exploração conhecida{{end}}. A partir daí, cada um pode entrar em atualização e em campanha.</p>
{{if .FilterTotal}}<p class="aviso atencao">O filtro tem {{.FilterTotal}} itens aguardando; este lote leva os {{.Max}} primeiros, pela ordem da lista. Os demais ficam para o próximo lote.</p>{{end}}
{{if .Skipped}}<p class="muted">{{.Skipped}} marcado(s) ficaram de fora: já aprovados, ou não estão mais pendentes.</p>{{end}}
<form method="post" action="/painel/patch/aprovar-lote">
<input type="hidden" name="confirmar" value="1">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Q}}<input type="hidden" name="q" value="{{.Q}}">{{end}}
<div class="card"><div class="card-cab"><h2>Itens do lote</h2><span class="muted">avaliação de agora</span></div>
<div class="tabela"><table>
<thead><tr><th>Item</th><th>Tipo</th><th>Corrigido em</th><th>Severidade</th><th>Explorada</th><th>Sistemas</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><input type="hidden" name="sel" value="{{.Sel}}"><code>{{.ID}}</code></td><td>{{.Kind}}</td><td><code>{{.FixedIn}}</code></td>
<td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td class="num">{{.Machines}}</td></tr>{{end}}</tbody></table></div>
</div>
<div class="card"><div class="card-cab"><h2>Aprovação</h2></div>
<div class="acao"><input name="nota" value="{{.Note}}" maxlength="{{.MaxNote}}" placeholder="Observação para todos os itens (opcional), ex.: testado no anel piloto em 10/10" aria-label="observação"></div>
<div class="lote"><button type="submit">Aprovar {{len .Rows}} {{if eq (len .Rows) 1}}item{{else}}itens{{end}}</button><a class="btn" href="{{.Back}}">Voltar sem aprovar</a>
<span class="muted">O servidor confere cada item de novo no clique: o que outra pessoa aprovou antes fica com a aprovação dela.</span></div>
</div></form>
{{template "fim"}}{{end}}

{{define "patch-correcao"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Itens da correção</a></p>
<h1>Aprovar a correção <code>{{.FixedIn}}</code> <span class="muted">› conferência</span></h1>
<p class="aviso atencao">{{.Items}} {{if eq .Items 1}}CVE será aprovada{{else}}CVEs serão aprovadas{{end}}{{if .Exploited}}, {{.Exploited}} com exploração conhecida{{end}}, em {{.Machines}} máquina(s) ({{.Systems}}). A partir daí, essa correção pode entrar em atualização e em campanha.</p>
<div class="grade2">
<div class="card"><div class="card-cab"><h2>Resumo</h2><span class="muted">avaliação de agora</span></div><dl class="dados">
<dt>Correção</dt><dd><code>{{.FixedIn}}</code></dd>
<dt>CVEs aguardando</dt><dd>{{.Items}}</dd>
<dt>Exploradas</dt><dd>{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}nenhuma conhecida{{end}}</dd>
<dt>Máquinas</dt><dd>{{.Machines}} · {{.Systems}}</dd>
{{range .Severities}}<dt>{{.Label}}</dt><dd>{{.N}}</dd>{{end}}
</dl></div>
<div class="card"><div class="card-cab"><h2>Amostra</h2><span class="muted">as exploradas e as mais graves primeiro</span></div>
<div class="tabela"><table><thead><tr><th>CVE</th><th>Severidade</th><th>Explorada</th><th>Máquinas</th></tr></thead>
<tbody>{{range .Sample}}<tr><td><code>{{.ID}}</code></td><td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td class="num">{{.Machines}}</td></tr>{{end}}</tbody></table></div>
{{if .More}}<p class="muted">E mais {{.More}}: <a href="{{.Back}}">a lista completa</a>.</p>{{end}}</div>
</div>
<form method="post" action="/painel/patch/correcao"><input type="hidden" name="fix" value="{{.FixedIn}}"><input type="hidden" name="confirmar" value="1"><input type="hidden" name="conferido" value="{{.Digest}}">
<div class="card"><div class="card-cab"><h2>Aprovação</h2></div>
<div class="acao"><input name="nota" value="{{.Note}}" maxlength="{{.MaxNote}}" placeholder="Observação para todas (opcional), ex.: KB testado no anel piloto em 10/10" aria-label="observação"></div>
<div class="lote"><button type="submit">Aprovar {{.Items}} {{if eq .Items 1}}CVE{{else}}CVEs{{end}} da correção</button><a class="btn" href="{{.Back}}">Voltar sem aprovar</a>
<span class="muted">O servidor refaz a lista no clique e só aprova se for a mesma desta conferência.</span></div>
</div></form>
{{template "fim"}}{{end}}

{{define "patch-item"}}{{template "topo" .}}
<p><a href="/painel/patch">← Patch</a></p>
<h1><code>{{.Item.ID}}</code> {{if .Item.Approval}}<span class="estado em_dia">aprovado</span>{{else}}<span class="estado reboot_pendente">aguardando aprovação</span>{{end}}</h1>
<div class="grade2">
<div class="card"><div class="card-cab"><h2>Item</h2></div><dl class="dados">
<dt>Tipo</dt><dd>{{if .Item.Kind}}{{.Item.Kind}}{{else}}—{{end}}</dd>
<dt>Corrigido em</dt><dd>{{if .Item.FixedIn}}<code>{{.Item.FixedIn}}</code>{{else}}—{{end}}</dd>
<dt>Severidade</dt><dd>{{if .Item.Severity}}{{.Item.Severity}}{{else}}—{{end}}</dd>
<dt>Explorada</dt><dd>{{if .Item.Exploited}}<span class="expl">sim</span>{{else}}não conhecida{{end}}</dd>
{{if .Item.Note}}<dt>Aviso</dt><dd>{{.Item.Note}}</dd>{{end}}
{{if .Item.Catalogs}}<dt>Catálogo</dt><dd>{{range $i, $c := .Item.Catalogs}}{{if $i}}, {{end}}{{$c}}{{end}}</dd>{{end}}
<dt>Sistemas afetados</dt><dd>{{len .Item.Machines}}</dd>
</dl></div>
<div class="card"><div class="card-cab"><h2>Aprovação</h2></div>
{{if .Item.Approval}}<dl class="dados"><dt>Aprovado por</dt><dd>{{.Item.Approval.ApprovedBy}}</dd><dt>Em</dt><dd>{{.ApprovedAt}}</dd>{{if .Item.Approval.Note}}<dt>Observação</dt><dd>{{.Item.Approval.Note}}</dd>{{end}}</dl>
{{if .CanRevoke}}<form class="acao" method="post" action="/painel/patch/revogar"><input type="hidden" name="id" value="{{.Item.ID}}"><input type="hidden" name="fix" value="{{.Item.FixedIn}}"><button class="perigo" type="submit">Revogar aprovação</button></form>{{end}}
{{else}}<p class="muted">Ainda não aprovado. A partir da Aula 13.2, só item aprovado entra em atualização e em campanha.</p>
{{if .CanApprove}}<form class="acao" method="post" action="/painel/patch/aprovar"><input type="hidden" name="id" value="{{.Item.ID}}"><input type="hidden" name="fix" value="{{.Item.FixedIn}}">
<input name="nota" maxlength="{{.MaxNote}}" placeholder="Observação (opcional), ex.: testado no anel piloto"><button type="submit">Aprovar</button></form>
{{else if not .Admin}}<p class="muted">Aprovar exige o perfil admin.</p>{{end}}{{end}}
{{if not .Pending}}<p class="aviso atencao">Este item não está pendente em nenhuma máquina na avaliação de agora.</p>{{end}}
</div></div>
{{if .Item.Machines}}<div class="card"><div class="card-cab"><h2>Sistemas afetados</h2></div><div class="tabela"><table>
<thead><tr><th>Máquina</th><th>Sistema</th><th>Catálogo</th><th>Instalado</th><th>Explorada</th></tr></thead>
<tbody>{{range .Item.Machines}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=pendencias">{{if .Name}}{{.Name}}{{else}}<code>{{.ID}}</code>{{end}}</a></td><td>{{.OS}}</td><td>{{.Catalog}}</td><td><code>{{.Installed}}</code></td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{template "fim"}}{{end}}

{{define "patch-nao-encontrado"}}{{template "topo" .}}
<h1>Item não encontrado</h1>
<p class="card">O item não está pendente em nenhuma máquina e não tem aprovação registrada. <a href="/painel/patch">Voltar a Patch</a>.</p>
{{template "fim"}}{{end}}

{{define "maquina"}}{{template "topo" .}}
<p><a href="/painel/inventario">← Inventário</a> · <a href="/painel/">Frota</a></p>
<h1>{{.Title}} <span class="estado {{.D.State}}">{{.Label}}</span></h1>
{{if .D.Reasons}}<ul class="motivos">{{range .D.Reasons}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{range .SourceAlerts}}<p class="aviso {{.Level}}" role="alert">{{.Text}}</p>{{end}}
<nav class="abas" aria-label="abas do equipamento">{{range .Tabs}}<a href="{{.Href}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}{{if .Count}} <span class="cont">{{.Count}}</span>{{end}}</a>{{end}}</nav>

{{if eq .Tab "resumo"}}
<div class="grade2">
<div class="card"><div class="card-cab"><h2>Equipamento</h2></div><dl class="dados">
<dt>Nome</dt><dd>{{.Name}}</dd>
<dt>ID</dt><dd><code>{{.D.MachineID}}</code></dd>
{{if .Category}}<dt>Categoria</dt><dd>{{.Category}}</dd>{{end}}
{{if .Model}}<dt>Fabricante e modelo</dt><dd>{{.Model}}</dd>{{end}}
{{if .Serial}}<dt>Número de série</dt><dd><code>{{.Serial}}</code></dd>{{end}}
{{if .Host.Has}}<dt>IP principal</dt><dd>{{.Host.Primary}}</dd>{{end}}
{{if .Host.UsersKnown}}<dt>Usuários conectados</dt><dd>{{if .Host.Users}}<ul class="usuarios">{{range .Host.Users}}<li>{{if .Name}}{{.Name}} <span class="muted"><code>{{.Account}}</code></span>{{else}}<code>{{.Account}}</code>{{end}} <span class="muted">· {{.Session}}{{if not .Active}} · desconectada{{end}}</span></li>{{end}}</ul><span class="muted">coletado {{.Host.Age}}</span>{{else}}ninguém <span class="muted">· coletado {{.Host.Age}}</span>{{end}}</dd>{{end}}
<dt>Último contato</dt><dd>{{.Seen}}</dd>
<dt>Inventário coletado</dt><dd>{{.Collected}}</dd>
<dt>Agente</dt><dd>{{if .D.AgentVersion}}{{.D.AgentVersion}}{{else}}—{{end}}</dd>
<dt>Canal</dt><dd>{{.Channel}}</dd>
{{if .Ring}}<dt>Anel</dt><dd>{{.Ring}}{{if .Admin}} <form class="inline" method="post" action="/painel/maquinas/{{.D.MachineID}}/anel"><select name="anel" aria-label="anel novo">{{range .Rings}}<option value="{{.}}" {{if eq . $.RingN}}selected{{end}}>{{.}}</option>{{end}}</select> <button class="sec" type="submit">Mudar de anel</button></form>{{end}}</dd>
<dt>Janela</dt><dd>{{if .Window}}{{.Window}}{{if .NextWindow}} · {{.NextWindow}}{{end}}{{else}}— (jobs com janela não podem ser criados para este anel){{end}}</dd>{{end}}
</dl></div>
<div class="card"><div class="card-cab"><h2>Conformidade</h2></div><dl class="dados">
<dt>Estado</dt><dd><span class="estado {{.D.State}}">{{.Label}}</span></dd>
<dt>Pendentes</dt><dd>{{if .Pending}}<a href="/painel/maquinas/{{.D.MachineID}}?aba=pendencias">{{.Pending}}</a>{{else}}nenhuma{{end}}</dd>
{{if .D.Target}}<dt>Resolve tudo</dt><dd>{{.D.Target}}</dd>{{end}}
<dt>Catálogo</dt><dd>{{if .D.Catalog}}{{.D.Catalog}}{{else}}—{{end}}</dd>
{{range .D.CatalogWarnings}}<dt>Atenção</dt><dd>{{.}}</dd>{{end}}
</dl></div>
</div>
{{if .OSParams}}<div class="card"><div class="card-cab"><h2>Sistema operacional</h2></div><dl class="dados">
{{range .OSParams}}<dt>{{.Label}}</dt><dd>{{if .Mono}}<code>{{.Value}}</code>{{else}}{{.Value}}{{end}}</dd>{{end}}
</dl></div>{{end}}
<div class="card"><div class="card-cab"><h2>Discos</h2>{{if .Host.Has}}<span class="muted">coletado {{.Host.Age}}</span>{{end}}</div>
{{if .Donuts}}<ul class="roscas">{{range .Donuts}}<li><svg class="rosca" viewBox="0 0 100 100" width="104" height="104" role="img" aria-label="{{.Mount}}: {{.Pct}}% ocupado">
<circle cx="50" cy="50" r="36" fill="none" stroke-width="12" class="fundo"/>
<circle cx="50" cy="50" r="36" fill="none" stroke-width="12" class="{{.Level}}" stroke-dasharray="{{.Dash}}" transform="rotate(-90 50 50)"/>
<text x="50" y="56" text-anchor="middle">{{.Pct}}%</text></svg>
<span><code>{{.Mount}}</code><br><span class="muted">{{.Free}} livres de {{.Total}}</span></span></li>{{end}}</ul>
{{else if .Host.Has}}<p class="muted">Nenhum volume local informado.</p>
{{else}}<p class="muted">O agente desta máquina ainda não manda rede e discos: a coleta chegou na Aula 12.1. Atualize o agente.</p>{{end}}
</div>
{{if .Links}}<div class="card"><div class="card-cab"><h2>Alertas de identidade</h2></div><ul>
{{range .Links}}<li>{{.Relation}}: {{if .Retired}}<code>{{.Short}}</code> (aposentada){{else}}<a href="/painel/maquinas/{{.ID}}"><code>{{.Short}}</code></a>{{end}}</li>{{end}}
</ul></div>{{end}}
{{if .Admin}}<section><h2>Aposentar</h2>
<p class="muted">A máquina sai da frota, do compliance e do painel, e a credencial dela deixa de valer. Nada é apagado; dá para restaurar em Aposentadas.</p>
<form class="acao" method="post" action="/painel/maquinas/{{.D.MachineID}}/aposentar">
<input name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="Motivo (ex.: registro antigo da VM reinstalada)">
<button class="perigo" type="submit">Aposentar</button></form></section>{{end}}
{{end}}

{{if eq .Tab "hardware"}}
<div class="card"><div class="card-cab"><h2>Hardware</h2>{{if .HasInventory}}<span class="grupo-btn"><a class="btn" href="/api/v1/machines/{{.D.MachineID}}/inventory">JSON</a></span>{{end}}</div>
{{if .HWParams}}<dl class="dados">{{range .HWParams}}<dt>{{.Label}}</dt><dd>{{if .Mono}}<code>{{.Value}}</code>{{else}}{{.Value}}{{end}}</dd>{{end}}</dl>
{{if .HWSources}}<p class="muted fontes">Fontes: {{range $i, $s := .HWSources}}{{if $i}}; {{end}}{{$s}}{{end}}</p>{{end}}
{{else}}<p class="muted">O inventário desta máquina não tem a seção de hardware.</p>{{end}}
</div>
<div class="card"><div class="card-cab"><h2>Rede e discos</h2>{{if .Host.Has}}<span class="muted">coletado {{.Host.Age}}</span>{{end}}</div>
{{if .Host.Has}}<dl class="dados">
<dt>IP principal</dt><dd>{{.Host.Primary}}</dd>
<dt>Check-in vindo de</dt><dd>{{.Host.Addr}}{{if .Host.AddrNote}} <span class="muted">· {{.Host.AddrNote}}</span>{{end}}</dd>
</dl>
{{if .Host.Disks}}<h3>Discos</h3><div class="tabela"><table>
<thead><tr><th>Volume</th><th>Sistema de arquivos</th><th>Tamanho</th><th>Livre</th><th>Ocupado</th></tr></thead>
<tbody>{{range .Host.Disks}}<tr><td><code>{{.Mount}}</code>{{if .Device}} <span class="muted">{{.Device}}</span>{{end}}{{if .Model}} <span class="muted">{{.Model}}{{if .Bus}} · {{.Bus}}{{end}}</span>{{end}}</td><td>{{.FS}}</td><td class="num">{{.Total}}</td><td class="num">{{.Free}}</td>
<td><span class="uso {{.Level}}" role="img" aria-label="{{.Pct}}% ocupado"><span style="width:{{.Pct}}%"></span></span> {{.Pct}}%</td></tr>{{end}}</tbody></table></div>{{end}}
{{if .Host.Ifs}}<h3>Interfaces</h3><div class="tabela"><table>
<thead><tr><th>Interface</th><th>Estado</th><th>MAC</th><th>IPv4</th><th>IPv6</th></tr></thead>
<tbody>{{range .Host.Ifs}}<tr><td>{{.Name}}</td><td>{{if .Up}}ativa{{else}}<span class="muted">inativa</span>{{end}}</td>
<td><code>{{.MAC}}</code>{{if .LocalMAC}} <span class="muted" title="MAC administrado localmente: bridge, VPN, container ou Wi-Fi com endereço aleatório">local</span>{{end}}</td><td>{{.IPv4}}</td><td>{{.IPv6}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{range .Host.Errors}}<p class="aviso atencao">{{.}}</p>{{end}}
{{else}}<dl class="dados"><dt>Check-in vindo de</dt><dd>{{.Host.Addr}}</dd></dl>
<p class="muted">O agente desta máquina ainda não manda rede e discos: a coleta chegou na Aula 12.1. Atualize o agente.</p>{{end}}
</div>
{{end}}

{{if eq .Tab "software"}}
<div class="card"><div class="card-cab"><h2>Software instalado</h2>{{if .Software.Collected}}<span class="muted">inventário coletado em {{.Software.Collected}}</span>{{end}}</div>
{{if .Software.Total}}<form class="busca" method="get" action="/painel/maquinas/{{.D.MachineID}}">
<input type="hidden" name="aba" value="software">
<input type="search" name="q" value="{{.Software.Q}}" maxlength="100" placeholder="Nome, fabricante ou versão" aria-label="buscar software">
<button type="submit">Buscar</button>{{if .Software.Q}} <a href="/painel/maquinas/{{.D.MachineID}}?aba=software">Limpar</a>{{end}}
</form>
{{if .Software.Rows}}<div class="tabela"><table>
<thead><tr><th>Nome</th><th>Versão</th><th>Fabricante</th><th>Instalado em</th><th>Escopo</th><th>Arquitetura</th></tr></thead>
<tbody>{{range .Software.Rows}}<tr><td>{{.Name}}{{if .Kind}} <span class="muted">· {{.Kind}}</span>{{end}}</td><td><code>{{.Version}}</code></td><td>{{.Publisher}}</td><td>{{.Installed}}</td><td>{{.Scope}}</td><td>{{.Arch}}</td></tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Software.Shown}}</span>{{if .Software.Prev}}<a href="{{.Software.Prev}}">‹ anterior</a>{{end}}{{if .Software.Next}}<a href="{{.Software.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum software com esse filtro.</p>{{end}}
{{else}}<p class="muted">{{if .HasInventory}}O inventário desta máquina não lista software.{{else}}Nenhum inventário recebido desta máquina.{{end}}</p>{{end}}
</div>
{{end}}

{{if eq .Tab "pendencias"}}
<div class="card"><div class="card-cab"><h2>Pendências</h2>{{if .Items}}<span class="grupo-btn"><a class="btn" href="/painel/maquinas/{{.D.MachineID}}/pendencias.csv">CSV</a><a class="btn" href="/api/v1/machines/{{.D.MachineID}}/compliance">JSON</a></span>{{end}}</div>
{{if .Items}}{{if .CanUpdate}}<form method="post" action="/painel/maquinas/{{.D.MachineID}}/atualizar">{{end}}<div class="tabela"><table>
<thead><tr>{{if .CanUpdate}}<th></th>{{end}}<th>Explorada</th><th>Item</th><th>Severidade</th><th>Instalado</th><th>Corrigido em</th><th>Aprovação</th><th>Observação</th></tr></thead>
<tbody>{{range .Items}}<tr>{{if $.CanUpdate}}<td>{{if .CanPick}}<input type="checkbox" name="item" value="{{.ID}}" aria-label="atualizar {{.ID}}" title="{{range $i, $p := .Packages}}{{if $i}}, {{end}}{{$p}}{{end}}">{{end}}</td>{{end}}
<td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td><code>{{.ID}}</code></td>
<td>{{.Severity}}</td><td>{{.Installed}}</td><td>{{.FixedIn}}</td>
<td>{{if .Approved}}<a href="{{.Href}}"><span class="estado em_dia">aprovado</span></a>{{else}}<a href="{{.Href}}">aguardando aprovação</a>{{end}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table></div>
{{if .NeedApproval}}<p class="aviso atencao">Nenhuma pendência desta máquina está aprovada. Só item aprovado em <a href="/painel/patch">Patch</a> pode ser atualizado: abra o item, confira os sistemas afetados e aprove.</p>{{end}}
{{if .CanUpdate}}<div class="lote">
<label><input type="radio" name="quando" value="agora" checked> agora</label>
{{if .Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do anel</label>{{end}}
<button type="submit">Atualizar marcados</button>
<span class="muted">Só as aprovadas em Patch podem ser marcadas. Instala os pacotes binários das fontes marcadas pelo apt ou pelo dnf; o agente confere na busca dele antes, e o resultado vem da busca seguinte. Não reinicia a máquina.</span>
</div></form>{{end}}
{{if .More}}<p class="muted">E mais {{.More}}: a lista completa está no CSV.</p>{{end}}
{{else}}<p class="muted">Nenhuma pendência na última avaliação.</p>{{end}}
</div>
{{with .WinUpdate}}<div class="card"><div class="card-cab"><h2>Atualizações do Windows pelo patchd</h2><span class="muted">busca de {{.ScanAt}}</span></div>
{{if .Problem}}<p class="aviso erro">{{.Problem}}</p>{{end}}
{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>Atualização</th><th>Corrige até</th><th>CVEs aprovadas</th><th>Aguardando aprovação</th><th>Na busca desta máquina</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><code>KB{{.KB}}</code>{{if and $.Admin .Approved}} <a class="muted" href="{{.CampaignHref}}">campanha por anel</a>{{end}}</td><td>{{.FixedIn}}</td><td class="num">{{.Approved}}{{if .Exploit}} <span class="expl">{{.Exploit}} explorada(s)</span>{{end}}</td>
<td class="num">{{if .Waiting}}<a href="{{.WaitingHref}}">{{.Waiting}}</a>{{else}}0{{end}}</td>
<td><span class="estado {{.Class}}">{{.Label}}</span>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma correção do Windows pendente nesta máquina na avaliação de agora.</p>{{end}}
{{if .Open}}<p class="muted">Instalação em aberto: <a href="/painel/maquinas/{{$.D.MachineID}}?aba=tarefas">job {{.Open}}</a>.</p>{{end}}
{{if .CanInstall}}<form class="lote" method="post" action="/painel/maquinas/{{$.D.MachineID}}/windows/instalar">
<label><input type="radio" name="quando" value="agora" checked> agora</label>
{{if $.Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do anel</label>{{end}}
<button type="submit">Instalar {{range $i, $k := .Installable}}{{if $i}}, {{end}}KB{{$k}}{{end}}</button>
<span class="muted">Instala pelo Windows Update da própria máquina, da fonte da política (WSUS), só os KBs com CVE aprovada em Patch que a busca mostra pendentes. Não reinicia: o reinício é o bloco abaixo. O resultado vem da busca seguinte.</span>
</form>{{end}}
</div>{{end}}
{{if or .ThirdParty .ThirdPartyCurrent}}<div class="card"><div class="card-cab"><h2>Programas de terceiros</h2><span class="muted">{{.ThirdPartyCurrent}} em dia</span></div>
{{if .ThirdParty}}<div class="tabela"><table>
<thead><tr><th>Programa</th><th>Como aparece no inventário</th><th>Instalada</th><th>Referência</th><th>Situação</th><th>Escopo</th></tr></thead>
<tbody>{{range .ThirdParty}}<tr><td><a href="{{.Href}}">{{.Program}}</a></td><td>{{.Name}}</td><td><code>{{.Installed}}</code></td><td>{{if .Reference}}<code>{{.Reference}}</code>{{else}}—{{end}}</td>
<td><span class="estado {{.Class}}">{{.Label}}</span>{{if .Origin}}<div class="muted nota">{{.Origin}}</div>{{end}}</td><td>{{.ScopeLabel}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Todos os programas de terceiros identificados estão em dia.</p>{{end}}
</div>{{end}}
{{with .TPUpdate}}<div class="card"><div class="card-cab"><h2>Atualização pelo patchd</h2><span class="muted">{{.Updates}} para atualizar</span></div>
<div class="tabela"><table>
<thead><tr><th>Programa</th><th>Instalado</th><th>Decisão</th><th>Ação</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><a href="{{.Href}}">{{.Program}}</a></td><td><code>{{.Installed}}</code></td>
<td><span class="estado {{.Class}}">{{.Label}}</span><div class="muted nota">{{.Reason}}</div></td>
<td>{{if .CanInstall}}<form method="post" action="/painel/maquinas/{{$.D.MachineID}}/terceiros/atualizar"><input type="hidden" name="app" value="{{.App}}"><button type="submit">Atualizar para {{.Target}}</button></form>{{else if .ActionHref}}<a href="{{.ActionHref}}">{{.Action}}</a>{{else if .Action}}<span class="muted">{{.Action}}</span>{{else}}—{{end}}</td></tr>{{end}}</tbody></table></div>
<p class="muted">O plano usa o último inventário e os instaladores guardados no servidor (o mesmo do <code>package plan</code>). Atualizar cria um job assinado: o agente baixa o instalador do servidor, confere o SHA-256 e a assinatura, instala em silêncio e manda o inventário; o job só conclui se o inventário mostrar a versão nova. Só versão aprovada na página do programa, uma instalação por vez.</p>
</div>{{end}}
{{if .CanReboot}}<section><h2>Reiniciar</h2>
<p class="muted">A última busca mostra reinício pendente. O agente confere de novo, avisa quem estiver na máquina (Windows: aviso na tela com o prazo; Linux: mensagem nos terminais e bloqueio de login nos 5 minutos finais) e reinicia; o resultado vem da busca depois do boot. Um administrador na máquina pode cancelar (<code>shutdown /a</code> no Windows, <code>shutdown -c</code> no Linux).</p>
{{if .Host.UsersKnown}}<p>{{if .Host.Users}}Conectados agora <span class="muted">(coletado {{.Host.Age}})</span>: {{range $i, $u := .Host.Users}}{{if $i}}; {{end}}{{if $u.Name}}{{$u.Name}}{{else}}<code>{{$u.Account}}</code>{{end}} <span class="muted">({{$u.Session}}{{if not $u.Active}}, desconectada{{end}})</span>{{end}}{{else}}Ninguém conectado <span class="muted">(coletado {{.Host.Age}})</span>.{{end}}</p>{{end}}
<form class="lote" method="post" action="/painel/maquinas/{{.D.MachineID}}/reiniciar">
{{if .Host.Active}}<label><input type="checkbox" name="ciente" value="1"> sei que há gente conectada ({{.Host.Active}} {{if eq .Host.Active 1}}sessão ativa{{else}}sessões ativas{{end}}) e quero reiniciar agora</label>{{end}}
<label>Aviso <select name="aviso">{{range .Delays}}<option value="{{.}}" {{if eq . 15}}selected{{end}}>{{.}} min</option>{{end}}</select></label>
<label><input type="radio" name="quando" value="agora" checked> agora</label>
{{if .Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do anel</label>{{end}}
<button type="submit">Reiniciar com aviso</button></form></section>{{end}}
{{end}}

{{if eq .Tab "tarefas"}}
<div class="card"><div class="card-cab"><h2>Tarefas</h2></div>
{{if .Admin}}{{if .JobsOn}}<form class="acao" method="post" action="/painel/maquinas/{{.D.MachineID}}/buscar">
<button type="submit">Buscar atualizações agora</button>
<span class="muted">Cria um job assinado; o agente busca no próximo check-in e o resultado aparece aqui.</span></form>
{{else}}<p class="muted">Pedir a busca pelo painel exige a chave de jobs no servidor (PATCHD_JOB_SIGNING_KEY_FILE).</p>{{end}}{{end}}
{{if .Jobs.Rows}}{{template "jobs-tabela" .Jobs}}{{else}}<p class="muted">Nenhum job para esta máquina.</p>{{end}}
</div>
{{if .CanHTTPS}}<section><h2>Passar para HTTPS</h2>
<p class="muted">O último check-in desta máquina chegou pela porta HTTP de transição. O job leva, assinado, o endereço <code>{{.PublicURL}}</code>; o agente entrega o resultado por ele (certificado conferido e credencial aceita) e só então grava o endereço novo. Se não der, ele continua no endereço atual e o motivo aparece no job.</p>
<form class="acao" method="post" action="/painel/maquinas/{{.D.MachineID}}/https"><button type="submit">Passar para HTTPS</button></form></section>{{end}}
{{end}}
{{template "fim"}}{{end}}

{{define "jobs-tabela"}}<div class="tabela"><table>
<thead><tr><th>Job</th>{{if not .MachineID}}<th>Máquina</th><th>Nome</th>{{end}}<th>Tipo</th><th>Estado</th><th>Janela</th><th>Criado</th><th>Por</th><th>Entregue</th><th>Encerrado</th><th>Detalhe</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td class="num">{{.ID}}</td>
{{if not $.MachineID}}<td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td>{{end}}
<td>{{.Type}}</td><td><span class="job {{.State}}">{{.StateLabel}}</span></td><td>{{.Window}}</td><td>{{.Created}}</td><td>{{.By}}</td>
<td>{{.Delivered}}</td><td>{{.Finished}}</td><td>{{.Detail}}</td>
{{if $.Admin}}<td>{{if .CanCancel}}<form method="post" action="/painel/jobs/{{.ID}}/cancelar">{{if $.MachineID}}<input type="hidden" name="maquina" value="{{$.MachineID}}">{{end}}<button class="perigo" type="submit">Cancelar</button></form>{{end}}</td>{{end}}
</tr>{{end}}</tbody></table></div>{{end}}

{{define "jobs"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Jobs</span></h1>
{{template "automacao-abas" "jobs"}}
<div class="card"><div class="card-cab"><h2>Mais recentes</h2><span class="muted">Os {{.N}} mais recentes da frota. Para pedir uma busca, abra a máquina na Frota.</span></div>
{{if .Jobs.Rows}}{{template "jobs-tabela" .Jobs}}{{else}}<p class="muted">Nenhum job ainda.</p>{{end}}</div>
{{template "fim"}}{{end}}

{{define "campanhas"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Campanhas</span></h1>
{{template "automacao-abas" "campanhas"}}
<p class="muted">O mesmo pedido passando pelos anéis em ordem: o servidor libera o anel seguinte quando o atual termina sem falha e o tempo de observação passa. Uma falha pausa a campanha até alguém decidir; um job adiado (programa aberto) ganha nova tentativa a cada hora, até 24.</p>
<div class="card"><div class="card-cab"><h2>Campanhas recentes</h2></div>{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>ID</th><th>Nome</th><th>Tipo</th><th>Alvo</th><th>Anéis</th><th>Estado</th><th>Detalhe</th><th>Criada</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="num"><a href="/painel/campanhas/{{.ID}}">{{.ID}}</a></td><td><a href="/painel/campanhas/{{.ID}}">{{.Name}}</a></td><td>{{.Type}}</td><td>{{.Sources}}</td><td><code>{{.Rings}}</code></td>
<td><span class="job {{.State}}">{{.Label}}</span></td><td>{{.Detail}}</td><td>{{.Created}} · {{.By}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma campanha ainda.</p>{{end}}</div>
{{if .NoKey}}<p class="muted">Criar campanhas pelo painel exige a chave de jobs no servidor (PATCHD_JOB_SIGNING_KEY_FILE).</p>{{end}}
{{if .CanNew}}<section id="nova"><h2>Nova campanha</h2>
<form class="campanha" method="post" action="/painel/campanhas/nova">
<div class="campo"><label for="nome">Nome</label><input id="nome" name="nome" type="text" required maxlength="100" value="{{.Form.Name}}" placeholder="ex.: curl e bluez, outubro"></div>
<fieldset class="campo tipos"><legend>Tipo</legend>
<label class="opcao"><input type="radio" name="tipo" value="rescan" {{if eq .Form.Type "rescan"}}checked{{end}}><span><b>Buscar atualizações</b><small>a máquina faz a busca agora; nada é instalado</small></span></label>
<label class="opcao"><input type="radio" name="tipo" value="update" {{if eq .Form.Type "update"}}checked{{end}}><span><b>Atualizar pacotes (Linux)</b><small>apt ou dnf, só os pacotes fonte indicados</small></span></label>
<label class="opcao"><input type="radio" name="tipo" value="windows_update" {{if eq .Form.Type "windows_update"}}checked{{end}}><span><b>Instalar atualizações do Windows</b><small>os KBs indicados, pelo WSUS; nunca reinicia</small></span></label>
<label class="opcao"><input type="radio" name="tipo" value="reboot" {{if eq .Form.Type "reboot"}}checked{{end}}><span><b>Reiniciar onde houver reinício pendente</b><small>com aviso a quem estiver conectado</small></span></label>
{{if .Form.TPOn}}<label class="opcao"><input type="radio" name="tipo" value="thirdparty" {{if eq .Form.Type "thirdparty"}}checked{{end}}><span><b>Instalar programa de terceiros (Windows)</b><small>a versão aprovada e guardada em Patch</small></span></label>{{end}}
</fieldset>
{{if .Form.TPOn}}<div class="campo so-terceiros"><label for="programa">Programa</label>{{if .Form.Programs}}<select id="programa" name="programa">{{range .Form.Programs}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<small class="muted">as versões aprovadas e guardadas</small>{{else}}<small class="muted">Nenhuma versão aprovada e guardada: aprove na página do programa, em Patch.</small>{{end}}</div>{{end}}
<div class="campo so-fontes"><label for="fontes">Fontes ou KBs</label><input id="fontes" name="fontes" type="text" maxlength="2000" value="{{.Form.Sources}}" placeholder="update: pacotes fonte, ex.: curl, bluez · Windows: KBs, ex.: KB5127648">
<small class="muted">separados por vírgula</small></div>
<div class="campo so-reinicio"><label for="aviso">Aviso do reinício</label><select id="aviso" name="aviso">{{range .Form.Delays}}<option value="{{.}}" {{if eq . $.Form.Delay}}selected{{end}}>{{.}} min</option>{{end}}</select>
<small class="muted">quanto tempo quem estiver conectado tem para salvar o trabalho</small></div>
<fieldset class="campo aneis"><legend>Anéis <small class="muted">em ordem, do menor para o maior</small></legend>
<div class="chips">{{range .Form.Rings}}{{if or .Machines .Checked}}<label class="anel"><input type="checkbox" name="anel" value="{{.Ring}}" {{if .Checked}}checked{{end}}> {{.Label}} <span class="muted">· {{.Machines}} máq.{{if .Window}} · janela {{.Window}}{{end}}</span></label>{{end}}{{end}}</div>
{{$vazios := 0}}{{range .Form.Rings}}{{if not (or .Machines .Checked)}}{{$vazios = 1}}{{end}}{{end}}
{{if $vazios}}<details class="vazios"><summary>Anéis sem máquinas</summary><div class="chips">{{range .Form.Rings}}{{if not (or .Machines .Checked)}}<label class="anel"><input type="checkbox" name="anel" value="{{.Ring}}"> {{.Label}} <span class="muted">· {{.Machines}} máq.{{if .Window}} · janela {{.Window}}{{end}}</span></label>{{end}}{{end}}</div></details>{{end}}
</fieldset>
<div class="campo"><label for="observacao">Observação</label><select id="observacao" name="observacao">{{range .Form.Observe}}<option value="{{.Hours}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<small class="muted">depois de um anel terminar sem falha, quanto esperar antes do próximo</small></div>
<fieldset class="campo quando"><legend>Quando</legend>
<label class="opcao"><input type="radio" name="quando" value="prazo" {{if ne .Form.When "janela"}}checked{{end}}><span><b>Já, com prazo de</b>
<select name="prazo" aria-label="prazo">{{range .Form.TTL}}<option value="{{.Hours}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></span></label>
<label class="opcao"><input type="radio" name="quando" value="janela" {{if eq .Form.When "janela"}}checked{{end}}><span><b>Na janela de cada anel</b><small>o job de cada máquina espera a janela do anel dela</small></span></label>
</fieldset>
<div class="acoes"><button type="submit">Conferir</button> <span class="muted">Nada é criado antes da conferência.</span></div>
</form></section>{{end}}
{{template "fim"}}{{end}}

{{define "campanha-conferir"}}{{template "topo" .}}
<h1>Conferir a campanha</h1>
<div class="card"><dl class="dados">
<dt>Nome</dt><dd>{{.Form.Name}}</dd>
<dt>Tipo</dt><dd>{{.TypeLabel}}{{if .SourcesList}} · {{if eq .Form.Type "windows_update"}}KBs{{else}}fontes{{end}}: {{.SourcesList}}{{end}}{{if .ProgramL}} · {{.ProgramL}}{{end}}{{if .Extra}} · {{.Extra}}{{end}}</dd>
<dt>Quando</dt><dd>{{.When}}</dd>
<dt>Observação</dt><dd>{{.ObserveL}} depois de cada anel concluído</dd>
</dl></div>
<div class="card"><div class="card-cab"><h2>Anéis, na ordem</h2></div><div class="tabela"><table><thead><tr><th>Ordem</th><th>Anel</th><th>Máquinas agora</th><th>Janela</th>{{if .ProgramL}}<th>Hoje</th>{{end}}<th></th></tr></thead>
<tbody>{{range $i, $r := .Rings}}<tr><td class="num">{{$i}}</td><td>{{$r.Label}}</td><td class="num">{{$r.Machines}}</td><td>{{$r.Window}}</td>{{if $.ProgramL}}<td>{{$r.Preview}}</td>{{end}}<td class="muted">{{$r.Note}}</td></tr>{{end}}</tbody></table></div></div>
{{range .Warnings}}<p class="aviso atencao">{{.}}</p>{{end}}
<p>Ao criar, o servidor libera o primeiro anel no próximo minuto: os jobs vão para máquinas de verdade.</p>
<form class="lote" method="post" action="/painel/campanhas/nova">
<input type="hidden" name="nome" value="{{.Form.Name}}"><input type="hidden" name="tipo" value="{{.Form.Type}}">
<input type="hidden" name="fontes" value="{{.Form.Sources}}"><input type="hidden" name="quando" value="{{.Form.When}}">
<input type="hidden" name="programa" value="{{.Form.Program}}"><input type="hidden" name="aviso" value="{{.Form.Delay}}">
<input type="hidden" name="observacao" value="{{.Form.ObserveH}}"><input type="hidden" name="prazo" value="{{.Form.TTLH}}">
{{range .Form.Chosen}}<input type="hidden" name="anel" value="{{.}}">{{end}}
<button type="submit" name="confirmar" value="1">Criar campanha</button>
<button class="sec" type="submit" name="corrigir" value="1">Corrigir</button>
</form>
{{template "fim"}}{{end}}

{{define "campanha"}}{{template "topo" .}}
<p><a href="/painel/campanhas">← Campanhas</a></p>
<h1>{{.Title}}: {{.C.Name}} <span class="job {{.State}}">{{.StateLabel}}</span></h1>
<div class="card"><dl class="dados">
<dt>Tipo</dt><dd>{{.TypeLabel}}</dd>
<dt>{{if eq .C.Type "thirdparty"}}Programa{{else if eq .C.Type "windows_update"}}KBs{{else if eq .C.Type "reboot"}}Reinício{{else}}Fontes{{end}}</dt><dd>{{.Sources}}</dd>
<dt>Quando</dt><dd>{{.When}}</dd>
<dt>Observação</dt><dd>{{.Observe}}</dd>
<dt>Detalhe</dt><dd>{{if .C.Detail}}{{.C.Detail}}{{else}}—{{end}}</dd>
{{if .C.Notes}}<dt>Liberações</dt><dd>{{range .C.Notes}}<div>{{.}}</div>{{end}}</dd>{{end}}
<dt>Criada</dt><dd>{{.Created}} por {{.C.CreatedBy}}</dd>
</dl>
{{if or .CanPause .CanResume .CanCancel}}<div class="lote">
{{if .CanPause}}<form method="post" action="/painel/campanhas/{{.C.ID}}/pausar"><button class="sec" type="submit">Pausar</button></form>{{end}}
{{if .CanResume}}<form method="post" action="/painel/campanhas/{{.C.ID}}/retomar"><button type="submit">Retomar (aceita as falhas do anel atual)</button></form>{{end}}
{{if .CanCancel}}<form method="post" action="/painel/campanhas/{{.C.ID}}/cancelar"><button class="perigo" type="submit">Cancelar a campanha</button></form>{{end}}
<span class="muted">Pausar e cancelar não desfazem jobs já entregues; cancelar cancela os pendentes.</span></div>{{end}}</div>
{{range .Rings}}<section><h2>Anel {{.Ring}}{{if .Current}} (atual){{end}}</h2>
{{if .Status}}<p class="muted">{{.Status}}</p>{{end}}
{{if .Jobs}}<div class="tabela"><table><thead><tr><th>Job</th><th>Máquina</th><th>Nome</th><th>Estado</th><th>Detalhe</th></tr></thead>
<tbody>{{range .Jobs}}<tr><td class="num">{{.ID}}</td><td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td>
<td><span class="job {{.State}}">{{.Label}}</span></td><td>{{.Detail}}</td></tr>{{end}}</tbody></table></div>{{end}}</section>{{end}}
{{template "fim"}}{{end}}

{{define "campanha-nao-encontrada"}}{{template "topo" .}}
<h1>Campanha não encontrada</h1>
<p><a href="/painel/campanhas">Voltar às campanhas</a>.</p>
{{template "fim"}}{{end}}

{{define "aneis"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Anéis e janelas</span></h1>
{{template "automacao-abas" "aneis"}}
<p class="muted">Anel 0 é o piloto e 1 é o padrão (máquina nova entra no 1); campanhas passam pelos anéis em ordem. A janela de manutenção de um anel é quando os jobs "na janela" daquele anel podem rodar. Para pôr máquinas num anel, marque-as na <a href="/painel/">Frota</a> ou use o detalhe da máquina.</p>
<div class="card"><div class="card-cab"><h2>Anéis em uso</h2><span class="muted">{{.Total}} máquina(s) na frota</span></div>{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>Anel</th><th>Máquinas</th><th>Janela</th><th>Próxima</th><th>Definida por</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td>{{.Label}}</td><td class="num">{{.Machines}}</td><td>{{.Window}}</td><td>{{.Next}}</td><td>{{.By}}{{if .HasWindow}} · {{.Updated}}{{end}}</td>
{{if $.Admin}}<td>{{if .HasWindow}}<form method="post" action="/painel/aneis/{{.Ring}}/remover-janela"><button class="perigo" type="submit">Remover janela</button></form>{{end}}</td>{{end}}</tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma máquina e nenhuma janela ainda.</p>{{end}}</div>
{{if .Admin}}<section><h2>Definir a janela de um anel</h2>
<p class="muted">Grava ou troca a janela. Uma janela que passa da meia-noite pertence ao dia em que começa (sex 22:00 por 6 h termina sábado às 04:00). Os jobs já criados mantêm a janela que levam assinada.</p>
<form class="grade" method="post" action="/painel/aneis/janela">
<label for="anel">Anel</label><select id="anel" name="anel">{{range .Rings}}<option value="{{.}}" {{if eq . 1}}selected{{end}}>{{.}}</option>{{end}}</select>
<span>Dias</span><span class="dias">{{range .Days}}<label><input type="checkbox" name="dia" value="{{.Value}}"> {{.Label}}</label>{{end}}</span>
<label for="inicio">Início</label><input id="inicio" name="inicio" type="time" required value="12:00">
<label for="duracao">Duração</label><select id="duracao" name="duracao">{{range .Durations}}<option value="{{.Minutes}}" {{if eq .Minutes 120}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="fuso">Fuso</label><input id="fuso" name="fuso" type="text" required maxlength="64" value="{{.Timezone}}" placeholder="America/Porto_Velho">
<span></span><span><button type="submit">Gravar janela</button></span>
</form></section>{{end}}
{{template "fim"}}{{end}}

{{define "aposentadas"}}{{template "topo" .}}
<h1>Máquinas aposentadas</h1>
<div class="card">{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>Máquina</th><th>Nome</th><th>SO</th><th>Aposentada em</th><th>Por</th><th>Motivo</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td><code>{{.Short}}</code></td><td>{{.Name}}</td><td>{{.OS}}</td><td>{{.When}}</td><td>{{.By}}</td><td>{{.Reason}}</td>
{{if $.Admin}}<td><form method="post" action="/painel/maquinas/{{.ID}}/restaurar"><button class="sec" type="submit">Restaurar</button></form></td>{{end}}</tr>{{end}}
</tbody></table></div>
{{else}}<p class="muted">Nenhuma máquina aposentada.</p>{{end}}</div>
{{template "fim"}}{{end}}

{{define "nao-encontrada"}}{{template "topo" .}}
<h1>Máquina não encontrada</h1>
<p class="card">Ela não existe, está aposentada, ou o estado de compliance está indisponível. <a href="/painel/">Voltar à frota</a>.</p>
{{template "fim"}}{{end}}

{{define "proibido"}}{{template "topo" .}}
<h1>Sem permissão</h1>
<p class="card">Esta ação é do perfil admin. <a href="/painel/">Voltar à frota</a>.</p>
{{template "fim"}}{{end}}
`))
