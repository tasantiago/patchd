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
	ParamsOn      bool   // Aula 19.2: a aba Parâmetros existe (servidor com o serviço de parâmetros)
	Refresh       int    // Aula 23.3: recarrega a página a cada N segundos (sem JavaScript); 0: não
	// CheckinWait: quanto um job pode esperar o check-in ("cerca de 15 min"), pelo intervalo de
	// Configuração › Parâmetros › Agentes.
	CheckinWait string
	// Evaluating: o aviso de que parte do parque ainda está em avaliação (evaluatingNotice).
	Evaluating string
}

func (a *api) base(r *http.Request, title, nav string) pageBase {
	s, _ := SessionFrom(r.Context())
	return a.baseFor(r.Context(), s, title, nav)
}

// baseFor: a base de uma página montada por uma ação de admin (a sessão vem do
// adminAction, não do contexto).
func (a *api) baseFor(ctx context.Context, s panel.Session, title, nav string) pageBase {
	b := pageBase{Brand: a.brandName(), Title: title, Session: s, Admin: s.Role == panel.RoleAdmin, Nav: nav,
		ServerVersion: a.serverVersion, Logo: a.logoURL(ctx), ParamsOn: a.params != nil, CheckinWait: a.checkinWait()}
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
	Rings       []int // grupos de implantação oferecidos no lote (admin, Aula 11.1)
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
	v := fleetView{pageBase: a.base(r, "Parque", "frota"), Rings: allRings()}
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
		a.logger.Error("falha interna", "op", "avaliar o parque", "error", err, "request_id", RequestID(r.Context()))
		v.Unavailable = "A avaliação do parque falhou; veja o log do servidor."
		a.render(w, r, http.StatusOK, "frota", v)
		return
	}
	evaluatingNotice(&v.pageBase, fleet)
	v.Total, v.EvaluatedAt = len(fleet.Machines), fleet.EvaluatedAt.Format("02/01/2006 15:04")+" UTC"
	// O grupo de implantação de cada máquina (Aula 11.1): a avaliação não traz, a lista de máquinas traz.
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
	MergedRecords, MergedBIOS int
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
	RingN                                int    // o grupo de implantação em número, para o formulário (Aula 11.1)
	Rings                                []int
	CanUpdate                            bool           // admin, chave de jobs e pendências do Linux aprovadas (Aulas 8.4 e 13.2)
	NeedApproval                         bool           // há pendências do Linux, mas nenhuma aprovada
	ThirdParty                           []tpMachineRow // programas de terceiros com problema (Aula 13.3)
	ThirdPartyCurrent                    int
	TPUpdate                             *tpUpdateCard // o plano de terceiros e o botão Atualizar (Aula 14.6)
	CanReboot                            bool          // admin, chave, Linux com reinício pendente (Aula 8.6)
	Delays                               []int
	RebootAt                             string           // sugestão do horário marcado (a próxima 02:00), como o datetime-local
	RebootValid                          []durationOption // por quanto tempo o reinício marcado vale
	PanelTZ                              string
	Window, NextWindow                   string
	Channel                              string // canal do último check-in (Aula 9.2)
	RealTime                             string // o canal em tempo real (vazio: desligado)
	Network                              string // docs/rele.md: dentro da rede ou pelo relé (vazio: sem check-in desde a 0064)
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
	FWParams      []fwRow         // Aula 21.1
	BIOS          *biosCard       // Aula 21.4: a guarda da senha (só na aba Hardware)
	BIOSConfig    *biosConfigCard // as configurações da BIOS (só na aba BIOS; docs/bios-config.md)
	Perf          *perfCard       // o desempenho (só na aba Desempenho; docs/monitoramento.md)
	Terminal      *terminalView   // o terminal do administrador (só admin, na aba Terminal; docs/terminal.md)
	Wake          *wakeCard       // Aula 22.1: ligar pela rede
	WakeOn        *wakeAction     // Aula 22.2: o botão Ligar e os pedidos
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
	members, err := a.store.MachineMergeMembers(ctx, id)
	if err != nil {
		a.internalError(w, r, "ler registros incorporados", err)
		return
	}
	for _, member := range members {
		if member.SourceID == id {
			continue
		}
		v.MergedRecords++
		guards, e := a.store.BIOSPasswords(ctx, member.SourceID)
		if e != nil {
			a.internalError(w, r, "ler guarda histórica", e)
			return
		}
		v.MergedBIOS += len(guards)
	}
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
		v.Flash = "Máquina devolvida ao parque; a credencial dela voltou a valer."
	case q.Has("anel-alterado"):
		n, _ := strconv.Atoi(q.Get("anel"))
		v.Flash = fmt.Sprintf("Máquina no grupo de implantação %d. Os jobs já criados não mudam; campanhas em andamento usam o grupo de implantação novo nos próximos grupos que liberarem.", n)
	case q.Get("erro") == "anel":
		v.Error = fmt.Sprintf("Escolha um grupo de implantação de 0 a %d.", store.MaxRing)
	case q.Get("erro") == "motivo":
		v.Error = fmt.Sprintf("Informe o motivo da aposentadoria (de %d a %d caracteres).", minReasonLength, maxReasonLength)
	case biosFlash(q, &v):
	case biosConfigFlash(q, &v):
	case wakeFlash(q, &v.pageBase):
	default:
		jobFlash(q, &v.pageBase, a.panelLocation())
	}
	v.JobsOn = a.jobKey != nil
	if err := a.ringView(r, id, &v); err != nil {
		a.internalError(w, r, "ler grupo de implantação e janela", err)
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
		loc := a.panelLocation()
		v.RebootAt, v.PanelTZ = nextNight(a.now(), loc).Format(scheduleInput), loc.String()
		for _, m := range rebootValidities {
			v.RebootValid = append(v.RebootValid, durationOption{Minutes: m, Label: durationLabel(m)})
		}
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
	if v.Tab == "hardware" {
		if v.BIOS, err = a.biosView(r, id, v.Admin); err != nil {
			a.internalError(w, r, "ler a guarda da BIOS", err)
			return
		}
	}
	if v.Tab == "desempenho" {
		if v.Perf, err = a.perfView(r, id, v.Admin); err != nil {
			a.internalError(w, r, "ler o desempenho", err)
			return
		}
	}
	if v.Tab == "terminal" && !v.Admin {
		v.Tab = "resumo" // o terminal é só do administrador
	}
	if v.Tab == "terminal" {
		if v.Terminal, err = a.terminalTabView(r, v.Session, id); err != nil {
			a.internalError(w, r, "ler o terminal", err)
			return
		}
		if v.Terminal.Refresh {
			v.Refresh = terminalRefresh
		}
		terminalFlash(r.URL.Query(), &v.pageBase)
	}
	if v.Tab == "bios" {
		if v.BIOSConfig, err = a.biosConfigView(r, id, v.Admin); err != nil {
			a.internalError(w, r, "ler as configurações da BIOS", err)
			return
		}
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
	if !v.Admin || !a.terminalOn() { // a aba Terminal: só para o admin, e só com o terminal ligado
		v.Tabs = slices.DeleteFunc(v.Tabs, func(t tabView) bool { return t.Label == "Terminal" })
	}
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
		v.Flash = "Máquina aposentada: saiu do parque, e a credencial dela deixou de valer."
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
	a.creds.reset() // a credencial da aposentada deixa de valer já
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

var pages = template.Must(template.New("").Funcs(template.FuncMap{"leia": leiaMais}).Parse(`
{{define "leia-mais"}}{{if .Long}}<details class="leia-mais"><summary><span class="curto">{{.Short}}</span> <span class="mais">leia mais</span><span class="menos">mostrar menos</span></summary><div class="todo">{{.Full}}</div></details>{{else}}{{.Full}}{{end}}{{end}}
{{define "topo"}}<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.Brand}}</title>{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}` + faviconLink + `
` + styleLink + `</head><body>
<input type="checkbox" id="menu" class="menu-chk" aria-label="recolher ou abrir o menu lateral">
<aside class="lado" aria-label="menu lateral">
<a class="marca" href="/painel/" title="{{.Brand}}">{{if .Logo}}<img src="{{.Logo}}" alt="" width="36" height="36">{{else}}<span class="escudo">` + shieldSVG + `</span>{{end}}<span class="rot">{{.Brand}}</span></a>
<div class="usuario" title="{{.Session.Greeting}} · {{.RoleLabel}}"><span class="avatar" aria-hidden="true">{{.Initials}}</span><span class="rot"><strong>{{.Session.Greeting}}</strong><small>{{.RoleLabel}} · {{.Session.SourceLabel}}</small></span></div>
<nav aria-label="principal">
<p class="grupo"><span class="rot">Navegação</span></p>
<a href="/painel/" title="Início" {{if eq .Nav "inicio"}}class="ativo" aria-current="page"{{end}}>` + ui.IcGeral + `<span class="rot">Início</span></a>
<a href="/painel/parque" title="Parque" {{if eq .Nav "frota"}}class="ativo" aria-current="page"{{end}}>` + ui.IcFrota + `<span class="rot">Parque</span></a>
<a href="/painel/inventario" title="Inventário" {{if eq .Nav "inventario"}}class="ativo" aria-current="page"{{end}}>` + ui.IcInventario + `<span class="rot">Inventário</span></a>
<a href="/painel/aposentadas" title="Aposentadas" {{if eq .Nav "aposentadas"}}class="ativo" aria-current="page"{{end}}>` + ui.IcAposentadas + `<span class="rot">Aposentadas</span></a>
<a href="/painel/alertas" title="Alertas de desempenho" {{if eq .Nav "alertas"}}class="ativo" aria-current="page"{{end}}>` + ui.IcAlertas + `<span class="rot">Alertas</span>{{with .Badges.Alerts}}<span class="cont" title="alertas de desempenho abertos e não reconhecidos">{{.}}</span>{{end}}</a>
<p class="grupo"><span class="rot">Patch</span></p>
<a href="/painel/patch?aba=geral" title="Patch › Visão geral" {{if and (eq .Nav "patch") (eq .Sub "geral")}}class="ativo" aria-current="page"{{end}}>` + ui.IcGeral + `<span class="rot">Visão geral</span>{{with .Badges.Sources}}<span class="cont" title="máquinas com a fonte de busca falhando">{{.}}</span>{{end}}</a>
<a href="/painel/patch" title="Patch › Sistema operacional" {{if and (eq .Nav "patch") (ne .Sub "geral") (ne .Sub "terceiros")}}class="ativo" aria-current="page"{{end}}>` + ui.IcSistema + `<span class="rot">Sistema</span></a>
<a href="/painel/patch?aba=terceiros" title="Patch › Programas de terceiros" {{if and (eq .Nav "patch") (eq .Sub "terceiros")}}class="ativo" aria-current="page"{{end}}>` + ui.IcTerceiros + `<span class="rot">Terceiros</span></a>
<p class="grupo"><span class="rot">Automação</span></p>
<a href="/painel/campanhas" title="Campanhas" {{if eq .Nav "campanhas"}}class="ativo" aria-current="page"{{end}}>` + ui.IcCampanhas + `<span class="rot">Campanhas</span>{{with .Badges.Paused}}<span class="cont" title="campanhas pausadas">{{.}}</span>{{end}}</a>
<a href="/painel/jobs" title="Jobs" {{if eq .Nav "jobs"}}class="ativo" aria-current="page"{{end}}>` + ui.IcJobs + `<span class="rot">Jobs</span></a>
<a href="/painel/grupos-implantacao" title="Grupos de implantação e janelas" {{if eq .Nav "aneis"}}class="ativo" aria-current="page"{{end}}>` + ui.IcAneis + `<span class="rot">Grupos de implantação</span></a>
<a href="/painel/autoatendimento" title="Autoatendimento" {{if eq .Nav "autoatendimento"}}class="ativo" aria-current="page"{{end}}>` + ui.IcLoja + `<span class="rot">Autoatendimento</span>{{with .Badges.Requests}}<span class="cont" title="pedidos aguardando a TI">{{.}}</span>{{end}}</a>
{{if .Admin}}<a href="/painel/padronizacao" title="Padronização" {{if eq .Nav "padronizacao"}}class="ativo" aria-current="page"{{end}}>` + ui.IcConfig + `<span class="rot">Padronização</span></a>{{end}}
{{if .Admin}}<a href="/painel/scripts" title="Scripts" {{if eq .Nav "scripts"}}class="ativo" aria-current="page"{{end}}>` + ui.IcScripts + `<span class="rot">Scripts</span></a>{{end}}
{{if .Admin}}<p class="grupo"><span class="rot">Administração</span></p>
<a href="/painel/descoberta" title="Descoberta de rede" {{if eq .Nav "descoberta"}}class="ativo" aria-current="page"{{end}}>` + ui.IcDescoberta + `<span class="rot">Descoberta</span></a>
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
{{if .Flash}}<p class="aviso ok" role="status">{{.Flash}}</p>{{end}}{{if .Evaluating}}<p class="aviso atencao" role="status">{{.Evaluating}}</p>{{end}}
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{end}}
{{end}}

{{define "fim"}}</main></div></body></html>{{end}}

{{define "parametros"}}<p class="muted">A tela vence: o valor gravado aqui vale no lugar do .env, em todos os servidores do mesmo banco (os outros percebem em até 30 segundos). "Restaurar o padrão" apaga o valor da tela e o .env volta a valer. Cada mudança vai para a auditoria com o antes e o depois{{if .NotifyOn}} e, por e-mail, para a equipe{{end}}. Banco, portas, TLS, chaves e segredos continuam no ambiente.</p>
{{range .Params}}<div class="card"><div class="card-cab"><h2>{{.Name}}</h2></div>
<form method="post" action="/painel/configuracao/parametros"><input type="hidden" name="grupo" value="{{.Name}}">
{{range .Fields}}<div class="param{{if .Problem}} problema{{end}}"><label for="p-{{.Key}}">{{.Label}}</label>
<div class="campo">{{if eq .Kind "multi"}}<input type="hidden" name="{{.Key}}" value="">{{$k := .Key}}{{$l := .Locked}}{{range .Options}}<label class="caixa"><input type="checkbox" name="{{$k}}" value="{{.Value}}"{{if .Selected}} checked{{end}}{{if $l}} disabled{{end}}> {{.Label}}</label>{{end}}
{{else if eq .Kind "select"}}<select id="p-{{.Key}}" name="{{.Key}}"{{if .Locked}} disabled{{end}}>{{range .Options}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>
{{else}}<input id="p-{{.Key}}" name="{{.Key}}" type="{{.Kind}}" value="{{.Value}}" maxlength="500"{{if .Locked}} disabled{{end}}>{{end}}{{if .Unit}}<span class="muted">{{.Unit}}</span>{{end}}</div>
<div class="info">{{if .Problem}}<span class="erro-campo" role="alert">{{.Problem}}</span>{{end}}{{if .Locked}}<span>{{.Locked}}</span>{{end}}
<span class="origem"><span class="fonte {{if eq .Source "tela"}}tela{{else if eq .Source ".env"}}env{{else}}padrao{{end}}">{{.Source}}</span>vale <b>{{.Show}}</b>{{if .Who}} · gravado por {{.Who}}{{end}}</span>
{{if .CanReset}}<span>Sem a tela: {{.EnvShow}} ({{.EnvSource}}) · <button class="link" type="submit" formaction="/painel/configuracao/parametros/restaurar" name="parametro" value="{{.Key}}">restaurar o padrão</button></span>{{end}}
{{if or .Help .Effect}}<span>{{.Help}}{{if .Effect}} Vale {{.Effect}}.{{end}}</span>{{end}}</div></div>
{{end}}<div class="param-salvar"><button type="submit">Salvar {{.Name}}</button>{{if and (eq .Name "Notificações") $.NotifyOn}}<button class="sec" type="submit" formaction="/painel/configuracao/notificacoes/teste" name="volta" value="parametros">Mandar e-mail de teste</button>{{end}}<span class="muted">grava só o que mudou neste cartão{{if eq .Name "Notificações"}}; o teste usa o que já está gravado{{end}}</span></div></form></div>
{{end}}{{end}}

{{define "frota"}}{{template "topo" .}}
<h1>Parque</h1>
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="kpis">
<li><a class="kpi{{if not .Filter}} ativo{{end}}" href="/painel/parque"><span class="kpi-rot">Máquinas ativas</span><span class="kpi-val">{{.Total}}</span></a></li>
{{range .Counts}}<li><a class="kpi{{if .Active}} ativo{{end}}" href="/painel/parque?estado={{.State}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{.Color}}">{{.N}}</span></a></li>{{end}}
<li><div class="kpi"><span class="kpi-rot">Com exploração conhecida</span><span class="kpi-val v-vermelho">{{.Exploited}}</span><span class="kpi-sub">máquinas com CVE explorada pendente</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Ainda por HTTP</span><span class="kpi-val v-laranja">{{.HTTP}}</span><span class="kpi-sub">último check-in sem TLS</span></div></li>
</ul>
<div class="card"><div class="card-cab"><h2>Máquinas{{if .Filter}} · {{.FilterLabel}}{{end}}</h2>
<span><span class="muted">Avaliado em {{.EvaluatedAt}}</span> <span class="grupo-btn"><a class="btn" href="/painel/parque.csv{{if .Filter}}?estado={{.Filter}}{{end}}">CSV</a><a class="btn" href="/api/v1/compliance">JSON</a></span></span></div>
{{if .Rows}}{{if .Admin}}<form method="post" action="/painel/grupos-implantacao/maquinas">{{if .Filter}}<input type="hidden" name="estado" value="{{.Filter}}">{{end}}{{end}}<div class="tabela"><table>
<thead><tr>{{if .Admin}}<th></th>{{end}}<th>Estado</th><th>Máquina</th><th>Nome</th><th>SO</th><th>Grupo de implantação</th><th>Pendentes</th><th>Exploradas</th><th>Último contato</th></tr></thead>
<tbody>{{range .Rows}}<tr>
{{if $.Admin}}<td><input type="checkbox" name="maquina" value="{{.ID}}" aria-label="marcar {{.Short}}"></td>{{end}}
<td><span class="estado {{.State}}">{{.Label}}</span>{{if .Reasons}}<ul class="motivos">{{range .Reasons}}<li>{{.}}</li>{{end}}</ul>{{end}}</td>
<td><a href="/painel/maquinas/{{.ID}}"><code>{{.Short}}</code></a></td>
<td>{{.Name}}</td><td>{{.OS}}</td><td class="num">{{.Ring}}</td><td>{{.Pending}}</td>
<td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td><td>{{.Seen}}</td></tr>
{{end}}</tbody></table></div>
{{if .Admin}}<div class="lote"><label>Pôr as marcadas no grupo de implantação <select name="anel">{{range .Rings}}<option value="{{.}}">{{.}}</option>{{end}}</select></label>
<button type="submit">Mover</button><span class="muted">0 = piloto, 1 = padrão. As janelas ficam em <a href="/painel/grupos-implantacao">Grupos de implantação</a>.</span></div></form>{{end}}
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
</ul>
<ul class="kpis" aria-label="firmware">{{range .FWCards}}
<li><a class="kpi{{if .Active}} ativo{{end}}" href="{{.Href}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val{{if .Class}} {{.Class}}{{end}}">{{.N}}</span><span class="kpi-sub">{{.Sub}}</span></a></li>{{end}}
</ul>
<div class="card"><div class="card-cab"><h2>Equipamentos</h2><span class="grupo-btn"><a class="btn" href="{{.CSV}}">CSV</a></span></div>
<form class="busca" method="get" action="/painel/inventario">
{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Firmware}}<input type="hidden" name="firmware" value="{{.Firmware}}">{{end}}
{{if .Buildings}}<select name="predio" aria-label="prédio"><option value="">Todos os prédios</option>{{range .Buildings}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>{{else if .Building}}<input type="hidden" name="predio" value="{{.Building}}">{{end}}
{{if ne .Order "nome"}}<input type="hidden" name="ordem" value="{{.Order}}">{{end}}{{if .Dir}}<input type="hidden" name="dir" value="{{.Dir}}">{{end}}
<select name="campo" aria-label="campo da busca">{{range .Fields}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Buscar" aria-label="texto da busca">
<label>Por página <select name="por">{{range .Pers}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="/painel/inventario">Limpar</a>{{end}}
</form>
<p class="muted nota">No campo IP, a busca aceita uma faixa: <code>10.1.2.0/24</code>, <code>10.1.2.10-10.1.2.50</code>, <code>10.1.2.10-50</code> ou <code>10.1.2.*</code>.{{if .Range}} Mostrando a {{.Range}}.{{end}}</p>
{{with .Batch}}<form class="lote" method="post" action="/painel/inventario/grupo">{{if .Q}}<input type="hidden" name="q" value="{{.Q}}"><input type="hidden" name="campo" value="{{.Field}}">{{end}}{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Firmware}}<input type="hidden" name="firmware" value="{{.Firmware}}">{{end}}{{if .Building}}<input type="hidden" name="predio" value="{{.Building}}">{{end}}<input type="hidden" name="n" value="{{.N}}">
<label>Pôr {{if eq .N 1}}a máquina encontrada{{else}}as {{.N}} máquinas encontradas{{end}} no grupo de implantação <select name="anel" required><option value="" selected disabled>escolha</option>{{range .Rings}}<option value="{{.}}">{{.}}{{if eq . 0}} (piloto){{else if eq . 1}} (padrão){{end}}</option>{{end}}</select></label> <button type="submit">Mover</button> <span class="muted">todas as do filtro, não só as desta página</span></form>{{end}}
{{if .Rows}}<div class="tabela"><table>
<thead><tr>{{range .Cols}}<th{{if .Sort}} aria-sort="{{.Sort}}"{{end}}><a href="{{.Href}}">{{.Label}}{{if .Arrow}} {{.Arrow}}{{end}}</a></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr>
<td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td>
<td>{{if .Tombo}}<code>{{.Tombo}}</code>{{else}}<span class="muted">—</span>{{end}}</td>
<td{{if .AssetHint}} title="{{.AssetHint}}"{{end}}>{{if .Building}}{{.Building}}{{if .Room}}<div class="muted nota">{{.Room}}</div>{{end}}{{else if .Room}}{{.Room}}{{else}}<span class="muted">—</span>{{end}}</td>
<td>{{if .Online}}<span class="estado em_dia">online</span>{{else}}<span class="estado sem_dado_recente">offline</span>{{end}}</td>
<td>{{.Agent}}</td>
<td>{{.IP}}{{if .IPFromCheckin}} <span class="muted" title="O agente ainda não manda as interfaces: é o endereço de onde veio o check-in.">(check-in)</span>{{end}}</td>
<td{{if .UserAll}} title="{{.UserAll}}"{{end}}>{{.User}}{{if .UserMore}} <span class="muted">+{{.UserMore}}</span>{{end}}</td>
<td>{{.Collected}}</td><td>{{.Seen}}</td><td>{{.Channel}}</td><td>{{.OS}}</td><td>{{.Category}}</td><td><code>{{.Serial}}</code></td><td{{if .BIOSMissing}} class="nivel-erro"{{end}}>{{.BIOS}}</td><td class="num">{{.Ring}}</td>
</tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>
{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}
{{range .Pages}}{{if .Gap}}<span>…</span>{{else if .Current}}<span class="atual" aria-current="page">{{.N}}</span>{{else}}<a href="{{.Href}}">{{.N}}</a>{{end}}{{end}}
{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum equipamento{{if .HasFilter}} com esse filtro{{end}}.</p>{{end}}
</div>
{{template "fim"}}{{end}}

{{define "patch-abas"}}<nav class="abas" aria-label="abas de Patch"><a href="/painel/patch?aba=geral"{{if eq . "geral"}} class="ativa" aria-current="page"{{end}}>Visão geral</a><a href="/painel/patch"{{if eq . "so"}} class="ativa" aria-current="page"{{end}}>Sistema operacional</a><a href="/painel/patch?aba=terceiros"{{if eq . "terceiros"}} class="ativa" aria-current="page"{{end}}>Programas de terceiros</a></nav>{{end}}

{{/* Automação (Aula 15.11): campanhas, jobs e grupos de implantação numa entrada só do menu, em abas. */}}
{{define "automacao-abas"}}<nav class="abas" aria-label="abas de Automação"><a href="/painel/campanhas"{{if eq . "campanhas"}} class="ativa" aria-current="page"{{end}}>Campanhas</a><a href="/painel/jobs"{{if eq . "jobs"}} class="ativa" aria-current="page"{{end}}>Jobs</a><a href="/painel/grupos-implantacao"{{if eq . "aneis"}} class="ativa" aria-current="page"{{end}}>Grupos de implantação e janelas</a><a href="/painel/autoatendimento"{{if eq . "autoatendimento"}} class="ativa" aria-current="page"{{end}}>Autoatendimento</a><a href="/painel/scripts"{{if eq . "scripts"}} class="ativa" aria-current="page"{{end}}>Scripts</a><a href="/painel/perguntas"{{if eq . "perguntas"}} class="ativa" aria-current="page"{{end}}>Perguntas ao parque</a></nav>{{end}}

{{define "configuracao"}}{{template "topo" .}}
<h1>Configuração</h1>
<nav class="abas" aria-label="abas da configuração"><a href="/painel/configuracao"{{if eq .Tab "sistema"}} class="ativa" aria-current="page"{{end}}>Sistema</a>{{if .HealthOn}}<a href="/painel/configuracao?aba=saude"{{if eq .Tab "saude"}} class="ativa" aria-current="page"{{end}}>Saúde</a>{{end}}{{if .ParamsOn}}<a href="/painel/configuracao?aba=parametros"{{if eq .Tab "parametros"}} class="ativa" aria-current="page"{{end}}>Parâmetros</a>{{end}}<a href="/painel/configuracao?aba=agente"{{if eq .Tab "agente"}} class="ativa" aria-current="page"{{end}}>Agente</a><a href="/painel/configuracao?aba=acesso"{{if eq .Tab "acesso"}} class="ativa" aria-current="page"{{end}}>Acesso</a><a href="/painel/configuracao?aba=auditoria"{{if eq .Tab "auditoria"}} class="ativa" aria-current="page"{{end}}>Auditoria</a><a href="/painel/configuracao?aba=autoatendimento"{{if eq .Tab "autoatendimento"}} class="ativa" aria-current="page"{{end}}>Autoatendimento</a><a href="/painel/configuracao?aba=patrimonio"{{if eq .Tab "patrimonio"}} class="ativa" aria-current="page"{{end}}>Patrimônio</a><a href="/painel/configuracao?aba=descoberta"{{if eq .Tab "descoberta"}} class="ativa" aria-current="page"{{end}}>Descoberta</a><a href="/painel/configuracao?aba=bios"{{if eq .Tab "bios"}} class="ativa" aria-current="page"{{end}}>BIOS</a></nav>
{{if eq .Tab "saude"}}{{with .Health}}
<div class="card saude-veredito borda-{{.Report.Level}}"><div class="card-cab"><h2>{{.Report.Verdict}}</h2><span class="muted">medindo desde {{.Since}}</span></div>
{{if .Report.Detail}}<p>{{.Report.Detail}}</p>{{end}}
{{if .Report.Advice}}<ol class="saude-recs">{{range .Report.Advice}}<li class="rec rec-{{.Level}}"><span class="rec-tag">{{.LevelText}}</span><div class="rec-corpo"><strong>{{.Resource}}: {{.Title}}</strong><p>{{.Why}}</p><p class="rec-acao"><b>O que fazer:</b> {{.Action}}</p></div></li>{{end}}</ol>{{end}}
{{if or .Report.Capacity .Report.Growth}}<p class="muted">{{.Report.Capacity}}{{if and .Report.Capacity .Report.Growth}} {{end}}{{.Report.Growth}}</p>{{end}}
</div>
<ul class="kpis saude-kpis" aria-label="recursos do servidor">{{range .Report.Gauges}}<li><div class="kpi"><span class="kpi-rot">{{.Resource}} <span class="saude-nivel nivel-{{.Level}}">{{.LevelText}}</span></span><span class="kpi-val">{{.Value}}</span>{{if ge .Pct 0}}<span class="medidor" aria-hidden="true"><span class="medidor-p med-{{.Level}} w{{.Pct}}"></span></span>{{end}}<span class="kpi-sub">{{.Sub}}</span></div></li>{{end}}</ul>
<div class="card"><div class="card-cab"><h2>Ao longo do tempo</h2><span class="muted">{{if .NoHistory}}sem banco: só as 24 horas deste processo{{else}}24 horas por minuto (memória do processo); 7 a 90 dias por hora (banco){{end}}</span></div>
<nav class="abas" aria-label="período">{{range .Periods}}<a href="{{.Href}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<div class="graficos">{{range .Charts}}{{template "perf-grafico" .}}{{end}}</div>
<p class="muted legenda-geral">{{if .Hourly}}Média por hora (a memória da máquina e o tempo de resposta, o pico da hora); a dica mostra também o pico do intervalo.{{else}}Uma medida por minuto; reiniciar o servidor zera as 24 horas (as horas fechadas ficam no banco).{{end}} A linha tracejada é a referência das regras. O tempo de resposta não conta os downloads de arquivo (catálogo, pacotes, versões do agente).</p>
</div>
<div class="card"><div class="card-cab"><h2>Onde o servidor gasta tempo</h2><span class="muted">desde a partida · {{.RoutesAll}} rotas</span></div>
{{if .Routes}}<div class="tabela"><table><thead><tr><th>Rota</th><th class="num">Requisições</th><th class="num">Média</th><th class="num">Maior</th><th class="num">Acima de 1 s</th><th class="num">Erros 5xx</th><th class="num">Do tempo total</th></tr></thead>
<tbody>{{range .Routes}}<tr><td><code>{{.Route}}</code></td><td class="num">{{.Count}}</td><td class="num{{if .Level}} nivel-{{.Level}}{{end}}">{{.Avg}}</td><td class="num">{{.Max}}</td><td class="num">{{.Slow}}</td><td class="num">{{.Errors}}</td><td class="num">{{.Share}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma requisição desde a partida.</p>{{end}}</div>
<div class="duas-col">
{{if .DB}}<div class="card"><div class="card-cab"><h2>Banco</h2><span class="muted">PostgreSQL</span></div>
<dl class="dados">{{range .DB}}<dt>{{.Label}}</dt><dd{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Value}}</dd>{{end}}</dl>
{{if .Tables}}<div class="tabela"><table><thead><tr><th>Maiores tabelas</th><th class="num">Tamanho</th></tr></thead><tbody>{{range .Tables}}<tr><td><code>{{.Name}}</code></td><td class="num">{{.Size}}</td></tr>{{end}}</tbody></table></div>{{end}}
</div>{{end}}
<div class="card"><div class="card-cab"><h2>Processo e disco</h2></div>
<dl class="dados">{{range .Process}}<dt>{{.Label}}</dt><dd{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Value}}</dd>{{end}}</dl>
{{if .Disks}}<div class="tabela"><table><thead><tr><th>Pasta</th><th class="num">Livre</th><th class="num">Total</th><th class="num">%</th></tr></thead>
<tbody>{{range .Disks}}<tr><td>{{.Name}}<br><code class="muted">{{.Dir}}</code></td>{{if .Note}}<td colspan="3" class="muted">{{.Note}}</td>{{else}}<td class="num">{{.Free}}</td><td class="num">{{.Total}}</td><td class="num{{if .Level}} nivel-{{.Level}}{{end}}">{{.Pct}}</td>{{end}}</tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma pasta de dados configurada (PATCHD_RELEASES_DIR, PATCHD_CONTENT_DIR, PATCHD_REPO_CACHE_DIR).</p>{{end}}
</div>
</div>
{{end}}{{end}}
{{if eq .Tab "bios"}}{{with .Fleet}}
<div class="card"><div class="card-cab"><h2>Padrão da BIOS</h2></div>
<p class="muted">Três objetivos para a frota inteira, de qualquer marca e modelo: senha de administrador pela guarda, Wake on LAN ligado e boot por USB desligado. A tela mostra quantas máquinas já estão conformes e aplica o que falta em lote, com conferência antes.</p>
<p><a href="/painel/bios/padrao">Abrir o padrão da BIOS →</a> <span class="muted">·</span> <a href="/painel/bios/lote">Senha da BIOS em lote →</a></p>
</div>
<div class="card" id="senha-padrao"><div class="card-cab"><h2>Senha padrão da BIOS</h2>{{if .On}}<span class="muted">chave de guarda <code>{{.KeyID}}</code></span>{{end}}</div>
<p class="muted">A senha que as máquinas de um fabricante já têm antes da primeira troca pela guarda (posta à mão ou pelo <code>.bat</code>). Com ela aqui, a primeira troca de cada máquina usa esta senha como a atual, sem digitar máquina por máquina. Fica cifrada para a chave de guarda, não aparece de novo nesta tela e só sai do servidor cifrada para a máquina, no job de troca. Depois da troca, cada máquina passa a ter a própria senha.</p>
{{if not .On}}<p class="aviso atencao">Guarda desligada: o servidor está sem a chave de guarda (<code>PATCHD_BIOS_ESCROW_KEY_FILE</code>; crie com <code>patchd-server bios keygen</code>).</p>{{end}}
<div class="tabela"><table><thead><tr><th>Fabricante</th><th>Senha padrão</th><th>Remover</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Label}}</td><td>{{if .Set}}cadastrada em {{.SetAt}} por {{.SetBy}}{{if .OtherKey}} <span class="nivel-atencao">· fechada com outra chave de guarda (<code>{{.OtherKey}}</code>): cadastre de novo</span>{{end}}{{else}}<span class="muted">não cadastrada</span>{{end}}</td>
<td>{{if .Set}}<form class="inline" method="post" action="/painel/configuracao/bios/senha-padrao/remover"><input type="hidden" name="fabricante" value="{{.Vendor}}"><input name="motivo" required minlength="{{$.Fleet.MinReason}}" maxlength="{{$.Fleet.MaxReason}}" placeholder="motivo" aria-label="motivo da remoção da senha padrão da {{.Label}}"> <button class="sec" type="submit">Remover</button></form>{{else}}—{{end}}</td></tr>{{end}}
</tbody></table></div>
{{if .On}}<h3>Cadastrar ou trocar</h3><form class="grade" method="post" action="/painel/configuracao/bios/senha-padrao" autocomplete="off">
<label for="padrao-fabricante">Fabricante</label><select id="padrao-fabricante" name="fabricante" required>{{range .Rows}}<option value="{{.Vendor}}"{{if eq .Vendor "lenovo"}} selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="padrao-senha">Senha padrão</label><input id="padrao-senha" type="password" name="senha" required maxlength="{{.MaxPassword}}" autocomplete="new-password">
<label for="padrao-confirmacao">Confirme a senha</label><input id="padrao-confirmacao" type="password" name="confirmacao" required maxlength="{{.MaxPassword}}" autocomplete="new-password">
<label for="padrao-motivo">Motivo</label><input id="padrao-motivo" name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="chamado, ordem de serviço...">
<span></span><span><button type="submit">Salvar senha padrão</button> <span class="muted">Gravar de novo troca a anterior. Na Lenovo: só letras sem acento, números e símbolos, sem espaço, vírgula ou ponto e vírgula.</span></span>
</form>{{end}}
</div>
{{end}}{{end}}
{{if eq .Tab "descoberta"}}{{with .Discovery}}
<div class="card"><div class="card-cab"><h2>Instalação automática do agente Windows</h2><span class="muted">Após cada varredura</span></div>
<p class="muted">Cadastre uma conta com administração local nas estações. O servidor usa WinRM HTTPS (porta 5986) para instalar nas máquinas Windows sem agente das faixas habilitadas.</p>
{{if .Blocker}}<p class="aviso atencao" role="status">{{.Blocker}}</p>{{else}}<p class="aviso ok">Instalação automática habilitada. Inicie uma varredura para procurar estações.</p>{{end}}
{{if not .KeyReady}}<details class="descoberta-ajuda"><summary>Como configurar a chave de cifragem</summary>
<p>No servidor, gere uma chave em uma pasta segura e configure o arquivo antes de reiniciar o serviço:</p>
<pre class="comando">patchd-server discovery keygen -out /caminho/seguro/discovery.key
PATCHD_DISCOVERY_KEY_FILE=/caminho/seguro/discovery.key</pre>
<p>No deploy Compose/Podman: PATCHD_DISCOVERY_KEY_HOST_FILE aponta para o arquivo no host e PATCHD_DISCOVERY_KEY_FILE=/run/patchd/discovery.key. O processo do servidor precisa de leitura. Guarde uma cópia segura da chave fora do banco.</p></details>{{end}}
<form method="post" action="/painel/configuracao/descoberta" class="descoberta-credencial" autocomplete="off">
<div class="descoberta-campos"><div class="descoberta-campo"><label for="descoberta-usuario">Usuário</label><input id="descoberta-usuario" name="usuario" value="{{.Username}}" maxlength="256" autocomplete="off" placeholder="DOMÍNIO\usuário" required{{if not .KeyReady}} disabled{{end}}><small class="muted">Conta de domínio ou conta administrativa local.</small></div>
<div class="descoberta-campo"><label for="descoberta-senha">Senha</label><input id="descoberta-senha" type="password" name="senha" maxlength="1024" autocomplete="new-password"{{if not .KeyReady}} disabled{{end}}><small class="muted">{{if .HasPassword}}Senha cadastrada. Deixe vazio para manter a senha.{{else}}A senha será cifrada antes de ser salva.{{end}}</small></div></div>
<label class="descoberta-opcao"><input type="checkbox" name="automatico"{{if .Enabled}} checked{{end}}{{if not .KeyReady}} disabled{{end}}><span>Instalar automaticamente após a varredura<small class="muted">Somente Windows sem agente, dentro das faixas habilitadas.</small></span></label>
<div class="descoberta-acoes"><button type="submit"{{if not .KeyReady}} disabled{{end}}>Salvar credencial</button><a class="btn sec" href="/painel/descoberta">Ver descoberta de rede</a></div>
</form>
<details class="descoberta-ajuda"><summary>Requisitos nas estações e no servidor</summary><ul><li>WinRM HTTPS habilitado na porta 5986, firewall permitindo acesso a partir do servidor e certificado confiável para o IP da estação.</li><li>Conta com permissão para sessão WinRM e instalação de serviços.</li><li>Agente Windows amd64 publicado em Configuração › Agente e PATCHD_PUBLIC_URL com a URL HTTPS alcançável pelas estações.</li></ul><p>Cada instalação usa um token de registro temporário de uso único. A senha não é exibida nem registrada na auditoria.</p></details></div>
{{end}}{{end}}
{{if eq .Tab "patrimonio"}}{{with .Assets}}
<div class="card"><div class="card-cab"><h2>Localização física</h2><span class="muted">do sistema de patrimônio, para o Inventário</span></div>
<p class="muted">O CSV que o sistema de patrimônio exporta, com o cabeçalho na primeira linha: <code>DEPREDIO</code>, <code>DESALA</code>, <code>UO</code>, <code>TOMBO</code>, <code>DEBEM</code>, <code>DESUBGRUPOBEM</code> (em qualquer ordem; só o <code>TOMBO</code> é obrigatório). Vírgula ou ponto e vírgula; UTF-8 ou o CSV do Excel. Cada importação <strong>substitui</strong> a anterior inteira. A máquina é ligada ao bem pelo nome: os dígitos do hostname, sem os zeros à esquerda, iguais ao tombo (FJ088594 é o tombo 88594; letras antes e depois variam).</p>
<form class="acao" method="post" action="/painel/configuracao/patrimonio" enctype="multipart/form-data"><input type="file" name="arquivo" accept=".csv,text/csv" required aria-label="arquivo CSV do patrimônio"><button type="submit">Importar</button><span class="muted">até {{.MaxMB}} MB</span></form>
</div>
<div class="card"><div class="card-cab"><h2>Ligação com o inventário</h2></div>
<dl class="dados"><dt>Bens no patrimônio</dt><dd>{{.Assets}}{{if .Assets}} <span class="muted">({{.AssetsWithMachine}} com máquina no inventário)</span>{{end}}</dd>
<dt>Máquinas no inventário</dt><dd>{{.Machines}} <span class="muted">({{.Matched}} com tombo ligado)</span></dd></dl>
{{if and .Assets .Unmatched}}<p class="muted">Máquinas sem tombo ligado (o nome não tem um número que esteja no patrimônio):</p>
<p>{{range $i, $m := .Unmatched}}{{if $i}}, {{end}}<a href="/painel/maquinas/{{$m.ID}}">{{$m.Name}}</a>{{end}}{{if .UnmatchedMore}} <span class="muted">e mais {{.UnmatchedMore}}</span>{{end}}</p>{{end}}
</div>
<div class="card"><div class="card-cab"><h2>Importações</h2><span class="muted">as últimas 5</span></div>
{{if .Imports}}<div class="tabela"><table><thead><tr><th>Quando</th><th>Quem</th><th>Arquivo</th><th>Bens</th><th>Sem tombo</th><th>Repetidos</th></tr></thead>
<tbody>{{range .Imports}}<tr><td>{{.At}}</td><td>{{.By}}</td><td>{{.File}}</td><td class="num">{{.Kept}}</td><td class="num">{{.Skipped}}</td><td class="num">{{.Duplicates}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma importação ainda.</p>{{end}}</div>
{{end}}{{end}}
{{if eq .Tab "autoatendimento"}}{{with .SelfService}}
{{if .NoManifest}}<p class="aviso atencao">O servidor está sem as regras de terceiros: o autoatendimento depende delas.</p>{{else}}
<div class="card"><div class="card-cab"><h2>O que o usuário pode pedir</h2></div>
<p class="muted">Os pedidos feitos e as recusas ficam em <a href="/painel/autoatendimento">Automação › Autoatendimento</a>. Por programa com instalação pelo patchd. Atualizar: quem já tem o programa leva para a versão aprovada mais nova. Instalar: quem não tem instala essa versão. Grupos do AD pelo sAMAccountName (GG-Exemplo ou DOMINIO\GG-Exemplo), separados por vírgula; vazio: qualquer usuário da máquina. O agente confere pelos grupos do token do Windows de quem pede, inclusive os aninhados. A versão vem sempre da aprovação em Patch: a oferta não aprova nada. Aprovação da TI (Aula 18.3): cada pedido espera alguém aprovar em Automação › Autoatendimento antes de virar job. Desmarcar atualizar e instalar tira o programa da loja.</p>
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
<div class="card"><div class="card-cab"><h2>Trilha de auditoria</h2><span class="grupo-btn"><a class="btn" href="/painel/terminal">Comandos do terminal</a> <a class="btn" href="{{.CSV}}">CSV</a></span></div>
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
{{if eq .Tab "parametros"}}{{template "parametros" .}}{{end}}
{{if eq .Tab "sistema"}}<div class="card"><div class="card-cab"><h2>Servidor</h2><span class="muted">só leitura: o que muda pela tela fica em Parâmetros; o resto, no ambiente (.env) e nos arquivos de segredo</span></div>
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
<label for="usos">Máquinas</label><input id="usos" name="usos" type="number" min="0" max="{{.MaxUses}}" value="1">
<span></span><label class="opcao-linha"><input type="checkbox" name="rele" value="1"> <span><b>Pelo relé</b> <small class="muted">para notebook que nunca entra na rede: uso único (ignora Máquinas), só vale pela porta do relé e vence em até 7 dias; o comando já leva o -relay</small></span></label>
<span></span><span><button type="submit">Criar token</button> <span class="muted">O token aparece uma única vez, com o comando de instalação. Máquinas: 0 permite usos ilimitados. Escolha Sem vencimento para um token permanente.</span></span>
</form>
{{if .Tokens}}<div class="tabela"><table><thead><tr><th>ID</th><th>Descrição</th><th>Criado</th><th>Vence</th><th>Usos</th><th>Estado</th><th></th></tr></thead>
<tbody>{{range .Tokens}}<tr><td class="num">{{.ID}}</td><td>{{.Note}}</td><td>{{.Created}}</td><td>{{.Expires}}</td><td class="num">{{.Uses}} de {{if .MaxUses}}{{.MaxUses}}{{else}}ilimitados{{end}}</td><td>{{.Status}}{{if .Relay}} · <span class="muted">pelo relé</span>{{end}}</td>
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
<p class="aviso atencao">Guarde agora: o token não será mostrado de novo. Validade: {{.Expires}}, para {{if .Uses}}{{.Uses}} máquina(s){{else}}usos ilimitados{{end}}{{if .Note}} ({{.Note}}){{end}}.</p>
{{if .Relay}}<p class="aviso">Token <b>pelo relé</b>: vale para uma máquina só, e só fora da rede, pelo relé ({{.Relay}}). Dentro da rede ele é recusado: use um token comum. O comando abaixo já leva o <code>-relay</code>; o agente tenta o servidor direto e, sem resposta, registra pelo relé.</p>{{end}}
<div class="card"><div class="card-cab"><h2>Token</h2></div><pre class="comando">{{.Token}}</pre></div>
<div class="card"><div class="card-cab"><h2>Windows</h2><span class="muted">{{if .Version}}patchd-agent {{.Version}} · {{end}}baixe o executável{{if .CA}} e o ca.crt{{end}} em Configuração › Agente</span></div><pre class="comando">{{.WinCommand}}</pre></div>
<div class="card"><div class="card-cab"><h2>Linux</h2></div><pre class="comando">{{.LinCommand}}</pre></div>
<p class="muted">O token vai num arquivo temporário apagado em seguida: na linha de comando ele ficaria no histórico e na lista de processos. O install é repetível: rodar de novo na mesma máquina não gasta outro uso.</p>
{{template "fim"}}{{end}}

{{define "patch-geral"}}{{template "topo" .}}
<h1>Patch <span class="muted">› Visão geral</span></h1>
{{template "patch-abas" "geral"}}
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
{{if .FleetSize}}<div class="card conformidade"><div class="card-cab"><h2>Conformidade do parque</h2></div>
<div class="conf-corpo"><a class="conf-num" href="/painel/parque?estado=em_dia"><span class="hero">{{.FleetPct}}%</span><span class="muted">em dia · {{.FleetOK}} de {{.FleetSize}} máquinas</span></a>
<div class="conf-barra">{{template "segbar" .Fleet}}{{template "estados-legenda"}}</div></div></div>{{end}}
<ul class="kpis">{{range .KPIs}}<li><a class="kpi" href="{{.Href}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{.Color}}">{{.Value}}</span><span class="kpi-sub">{{.Sub}}</span></a></li>{{end}}</ul>
<div class="card"><div class="card-cab"><h2>Precisa de atenção</h2><span class="muted">avaliado em {{.EvaluatedAt}}</span></div>
{{if .Attention}}<ul class="atencao-lista">{{range .Attention}}<li class="{{.Level}}"><a href="{{.Href}}">{{.Text}}</a></li>{{end}}</ul>
{{else}}<p class="muted">Nada pede atenção agora.</p>{{end}}</div>
<div class="duas-col">
<div class="card"><div class="card-cab"><h2>Por grupo de implantação</h2><a class="muted" href="/painel/grupos-implantacao">Grupos de implantação e janelas</a></div>
{{if .Rings}}<ul class="seg-linhas empilhada">{{range .Rings}}<li><span class="seg-nome">Grupo de implantação {{.Ring}}<small>{{.Machines}} máquina(s)</small></span>{{template "segbar" .Bar}}<span class="seg-fim">{{.Pct}}% em dia</span></li>{{end}}</ul>
{{template "estados-legenda"}}<details class="tabela-grafico"><summary>Ver em tabela</summary><div class="tabela"><table><thead><tr><th>Grupo de implantação</th><th>Máquinas</th><th>Em dia</th><th>Faltando</th><th>Reinício</th><th>Sem contato</th><th>Outros</th><th>% em dia</th></tr></thead>
<tbody>{{range .Rings}}<tr><td>{{.Ring}}</td><td class="num">{{.Machines}}</td><td class="num">{{.OK}}</td><td class="num">{{.Missing}}</td><td class="num">{{.Reboot}}</td><td class="num">{{.Stale}}</td><td class="num">{{.NA}}</td>
<td class="nowrap"><span class="barra" title="{{.Pct}}% em dia"><span class="w{{.Pct}}"></span></span> <span class="muted">{{.Pct}}%</span></td></tr>{{end}}</tbody></table></div>
</details>
{{else}}<p class="muted">Nenhuma máquina avaliada.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Por sistema</h2></div>
{{if .Systems}}<ul class="seg-linhas empilhada">{{range .Systems}}<li><span class="seg-nome">{{.OS}}<small>{{.Machines}} máquina(s){{if .Pending}} · {{.Pending}} pendentes{{end}}{{if .Exploited}} · <span class="expl">{{.Exploited}} exploradas</span>{{end}}</small></span>{{template "segbar" .Bar}}<span class="seg-fim">{{.Pct}}% em dia</span></li>{{end}}</ul>
{{template "estados-legenda"}}<details class="tabela-grafico"><summary>Ver em tabela</summary><div class="tabela"><table><thead><tr><th>Sistema</th><th>Máquinas</th><th>Em dia</th><th>Pendentes</th><th>Exploradas</th><th>% em dia</th></tr></thead>
<tbody>{{range .Systems}}<tr><td>{{.OS}}</td><td class="num">{{.Machines}}</td><td class="num">{{.OK}}</td><td class="num">{{.Pending}}</td><td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td>
<td class="nowrap"><span class="barra" title="{{.Pct}}% em dia"><span class="w{{.Pct}}"></span></span> <span class="muted">{{.Pct}}%</span></td></tr>{{end}}</tbody></table></div>
</details>
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
{{if .Exec}}<ul class="seg-linhas exec">{{range .Exec}}<li><span class="seg-nome">{{.Type}}</span><div class="seg-trilho">{{template "segbar" .Bar}}</div><span class="seg-fim">{{.Created}} criados</span></li>{{end}}</ul>
<ul class="seg-legenda"><li><i class="st-ok"></i>Concluídos</li><li><i class="st-erro"></i>Sem sucesso</li><li><i class="st-sem"></i>Adiados ou dispensados</li><li><i class="st-aberto"></i>Em aberto</li></ul>
<p class="muted">{{.ExecTotal.Created}} jobs criados: {{.ExecTotal.Done}} concluídos, {{.ExecTotal.Failed}} sem sucesso, {{.ExecTotal.Postponed}} adiados, {{.ExecTotal.Open}} em aberto. O comprimento da barra compara o volume entre os tipos.</p>
<details class="tabela-grafico"><summary>Ver em tabela</summary><div class="tabela"><table><thead><tr><th>Tipo</th><th>Criados</th><th>Concluídos</th><th>Sem sucesso</th><th>Adiados</th><th>Em aberto</th></tr></thead>
<tbody>{{range .Exec}}<tr><td>{{.Type}}</td><td class="num">{{.Created}}</td><td class="num">{{.Done}}</td><td class="num">{{.Failed}}</td><td class="num">{{.Postponed}}</td><td class="num">{{.Open}}</td></tr>{{end}}
<tr class="total"><td>Total</td><td class="num">{{.ExecTotal.Created}}</td><td class="num">{{.ExecTotal.Done}}</td><td class="num">{{.ExecTotal.Failed}}</td><td class="num">{{.ExecTotal.Postponed}}</td><td class="num">{{.ExecTotal.Open}}</td></tr></tbody></table></div>
</details>
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
<li><a class="kpi{{if eq .Inst "aguardando"}} ativo{{end}}" href="{{.WaitingHref}}"><span class="kpi-rot">Instaladores aguardando aprovação</span><span class="kpi-val v-laranja">{{.Waiting}}</span><span class="kpi-sub">versão guardada mais nova</span></a></li>
</ul>
<div class="card"><div class="card-cab"><h2>Programas{{if not .All}} com problema{{end}}</h2><span class="muted">{{if .All}}<a href="{{.ProblemsHref}}">só os com problema</a>{{else}}<a href="{{.AllHref}}">mostrar todos os identificados</a>{{end}}</span></div>
<p class="muted">Os programas com regra de instalação o patchd atualiza: baixe a versão na página do programa, aprove (aqui em lote, ou lá) e crie a campanha ou o job. Os outros, a lista diz o que distribuir e onde. A referência vem do catálogo de terceiros (winget e feeds dos fabricantes).</p>
<form class="busca" method="get" action="/painel/patch"><input type="hidden" name="aba" value="terceiros">{{if .All}}<input type="hidden" name="todos" value="1">{{end}}
<select name="instalador" aria-label="instalador">{{range .Insts}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Programa (chrome, 7-zip…)" aria-label="buscar programa">
<label>Por página <select name="por">{{range .Pers}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="{{.ClearHref}}">Limpar</a>{{end}}</form>
{{if .Rows}}{{if .CanBatch}}<form method="post" action="/painel/patch/terceiros/aprovar-lote">{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}{{end}}
<div class="tabela"><table>
<thead><tr>{{if .CanBatch}}<th class="col-lote">Lote</th>{{end}}<th>Programa</th><th>Referência</th><th>Instalações</th><th>Desatualizadas</th><th>Fora de suporte</th><th>Versões instaladas</th><th>Instalador</th><th>Origem</th></tr></thead>
<tbody>{{range .Rows}}<tr>{{if $.CanBatch}}<td class="col-lote">{{if .Sel}}<input type="checkbox" name="sel" value="{{.Sel}}" aria-label="marcar {{.Program}}">{{end}}</td>{{end}}<td><a href="{{.Href}}">{{.Program}}</a></td><td><code>{{.Reference}}</code></td><td class="num">{{.Installs}}</td>
<td class="num">{{if .Outdated}}<span class="v-laranja">{{.Outdated}}</span>{{else}}0{{end}}</td><td class="num">{{if .EOL}}<span class="expl">{{.EOL}}</span>{{else}}0{{end}}</td>
<td><code>{{.Versions}}</code></td><td>{{if .Installer}}<span class="estado {{.InstallerClass}}">{{.Installer}}</span>{{end}}{{if .InstallerNote}} <span class="muted">{{.InstallerNote}}</span>{{end}}</td><td class="muted">{{.Origin}}</td></tr>{{end}}</tbody></table></div>
{{if .CanBatch}}<div class="lote"><button type="submit">Conferir marcados</button>
<button class="sec" type="submit" name="filtro" value="1">Conferir todos os {{.FilterWaiting}} aguardando{{if .HasFilter}} deste filtro{{end}}</button>
<span class="muted">Aprova a versão guardada mais nova de cada programa. Nada é aprovado antes da conferência.{{if gt .FilterWaiting .MaxBatch}} Um lote leva até {{.MaxBatch}} programas.{{end}}</span></div></form>{{end}}
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum programa de terceiros {{if .HasFilter}}com esse filtro{{else if .All}}identificado{{else}}com problema{{end}} no parque.</p>{{end}}
</div>{{end}}
{{template "fim"}}{{end}}

{{define "patch-terceiros-lote"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Programas de terceiros</a></p>
<h1>Aprovar instaladores em lote <span class="muted">› conferência</span></h1>
<p class="aviso atencao">{{len .Rows}} {{if eq (len .Rows) 1}}versão será aprovada{{else}}versões serão aprovadas{{end}}. A partir daí, o patchd pode instalá-las onde o plano diz atualizar, por campanha ou job.</p>
{{if .FilterTotal}}<p class="aviso atencao">O filtro tem {{.FilterTotal}} programas aguardando; este lote leva os {{.Max}} primeiros, pela ordem da lista. Os demais ficam para o próximo lote.</p>{{end}}
{{if .Skipped}}<p class="muted">{{.Skipped}} marcado(s) ficaram de fora: já aprovados, excluídos, ou fora do parque.</p>{{end}}
<form method="post" action="/painel/patch/terceiros/aprovar-lote">
<input type="hidden" name="confirmar" value="1">{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}
<div class="card"><div class="card-cab"><h2>Versões do lote</h2><span class="muted">instaladores guardados no servidor</span></div>
<div class="tabela"><table>
<thead><tr><th>Programa</th><th>Versão a aprovar</th><th>Aprovada hoje</th><th>Instalações</th><th>Com problema</th></tr></thead>
<tbody>{{range .Rows}}<tr><td><input type="hidden" name="sel" value="{{.Sel}}">{{.Program}}</td><td><code>{{.Version}}</code></td><td><code>{{.Approved}}</code></td>
<td class="num">{{.Installs}}</td><td class="num">{{.Problems}}</td></tr>{{end}}</tbody></table></div>
</div>
<div class="card"><div class="card-cab"><h2>Aprovação</h2></div>
<div class="acao"><input name="nota" value="{{.Note}}" maxlength="{{.MaxNote}}" placeholder="Observação para todas as versões (opcional), ex.: testado no grupo de implantação piloto" aria-label="observação"></div>
<div class="lote"><button type="submit">Aprovar {{len .Rows}} {{if eq (len .Rows) 1}}versão{{else}}versões{{end}}</button><a class="btn" href="{{.Back}}">Voltar sem aprovar</a>
<span class="muted">O servidor confere cada versão de novo no clique: o que outra pessoa aprovou antes fica com a aprovação dela.</span></div>
</div></form>
{{template "fim"}}{{end}}

{{define "patch-programa"}}{{template "topo" .}}
<p><a href="/painel/patch?aba=terceiros">← Programas de terceiros</a></p>
<h1>{{.P.Program}}</h1>
<div class="card"><dl class="dados">
<dt>Referência</dt><dd>{{if .P.Reference}}<code>{{.P.Reference}}</code>{{else}}—{{end}}</dd>
{{if .P.Origin}}<dt>Origem</dt><dd>{{.P.Origin}}</dd>{{end}}
<dt>Instalações</dt><dd>{{len .P.Installs}} ({{.P.Outdated}} desatualizadas, {{.P.EOL}} fora de suporte)</dd>
</dl></div>
{{if .Install}}<div class="card"><div class="card-cab"><h2>Instaladores guardados</h2><span class="muted">winget <code>{{.Install.Winget}}</code>{{if .Install.Type}} · tipo {{.Install.Type}}{{end}} · assinatura {{if .Install.Signer}}exigida: {{.Install.Signer}}{{else}}<strong>não exigida</strong>: {{.Install.Unsigned}}{{end}}{{if .Install.Processes}} · não instala com {{.Install.Processes}} aberto{{end}}</span></div>
{{if .Versions}}<div class="tabela"><table>
<thead><tr><th>Versão</th><th>Instaladores</th><th>Aprovação</th></tr></thead>
<tbody>{{range .Versions}}<tr><td><code>{{.Version}}</code></td>
<td>{{range .Packages}}<div>{{.Type}} · {{.Arch}} · {{.Size}} · <code>{{.SHA}}</code> <span class="muted">{{.Fetched}}</span></div>{{end}}</td>
<td>{{if .Approved}}<span class="estado em_dia">aprovado</span> <span class="muted">{{.ApprovedBy}} · {{.ApprovedAt}}</span>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}{{if .CampaignHref}} <a class="btn" href="{{.CampaignHref}}">Campanha por grupo de implantação</a>{{end}}{{if .CanRevoke}}<form class="inline" method="post" action="/painel/patch/terceiros/revogar"><input type="hidden" name="app" value="{{$.P.AppID}}"><input type="hidden" name="version" value="{{.Version}}"><button class="perigo" type="submit">Revogar</button></form>{{end}}
{{else}}<span class="estado reboot_pendente">aguardando</span>{{if .CanApprove}}<form class="acao" method="post" action="/painel/patch/terceiros/aprovar"><input type="hidden" name="app" value="{{$.P.AppID}}"><input type="hidden" name="version" value="{{.Version}}"><input name="nota" maxlength="{{$.MaxNote}}" placeholder="Observação (opcional), ex.: testado no grupo de implantação piloto"><button type="submit">Aprovar</button></form>{{end}}{{end}}
{{if .CanDelete}}<details class="excluir"><summary>Excluir esta versão</summary><form method="post" action="/painel/patch/terceiros/excluir"><input type="hidden" name="app" value="{{$.P.AppID}}"><input type="hidden" name="version" value="{{.Version}}"><button class="perigo" type="submit">Excluir os instaladores da {{.Version}}</button> <span class="muted">dá para baixar de novo depois</span></form></details>{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum instalador guardado.{{if not .CanFetch}} No servidor: <code>patchd-server package fetch -app {{.P.AppID}}</code>.{{end}}</p>{{end}}
{{with .Fetch}}<div class="aviso {{if .Running}}atencao{{else if .OK}}ok{{else}}erro{{end}}"><strong>{{if .Running}}Baixando{{else if .OK}}Download concluído{{else}}O download não deu certo{{end}}</strong> · {{if .Version}}versão {{.Version}}{{else}}a mais nova do winget{{end}}{{if .Kind}} · {{.Kind}}{{end}} · pedido por {{.By}}{{if .Running}} · a página se atualiza sozinha{{end}}
{{if .Lines}}<pre class="comando andamento">{{range .Lines}}{{.}}
{{end}}</pre>{{end}}</div>{{end}}
{{if .CanFetch}}{{if .OtherBusy}}<p class="muted">Baixando outro programa agora ({{.OtherBusy}}); um download por vez.</p>{{else if not (and .Fetch .Fetch.Running)}}<form class="acao" method="post" action="/painel/patch/terceiros/baixar"><input type="hidden" name="app" value="{{.P.AppID}}"><input name="versao" maxlength="64" placeholder="Versão do winget (vazio: a mais nova)" aria-label="versão a baixar">{{if .Kinds}}<select name="tipo" aria-label="tipo do instalador"><option value="">{{if .RuleType}}o da regra ({{.RuleType}}){{else}}o primeiro do manifesto{{end}}</option>{{range .Kinds}}<option value="{{.Value}}">{{.Label}}</option>{{end}}</select>{{end}}<button type="submit">Baixar</button></form>{{if .Kinds}}<p class="muted">O tipo segue o que está instalado nas máquinas: uma instalação MSI só é atualizada por um MSI, e uma exe por um exe (a Atualização pelo patchd, na máquina, diz qual falta). A aprovação é da versão e vale para os dois.</p>{{end}}{{end}}{{end}}
{{if not .Admin}}<p class="muted">Aprovar, baixar e excluir exigem o perfil admin.</p>{{end}}
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
<div class="card"><div class="card-cab"><h2>{{if eq .View "correcao"}}Correções pendentes{{else}}Itens pendentes{{end}}</h2><span class="muted">avaliação de agora, contra o catálogo</span></div>
<nav class="abas" aria-label="visão da lista"><a href="{{.ViewItem}}"{{if ne .View "correcao"}} class="ativa" aria-current="page"{{end}}>Por item (pacote ou CVE)</a><a href="{{.ViewFix}}"{{if eq .View "correcao"}} class="ativa" aria-current="page"{{end}}>Por correção (KB, versão, pacote)</a></nav>
<form class="busca" method="get" action="/painel/patch">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .View}}<input type="hidden" name="ver" value="{{.View}}">{{end}}
<select name="sistema" aria-label="sistema">{{range .Systems}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<select name="severidade" aria-label="severidade">{{range .Sevs}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Item, correção ou aviso (curl, CVE-2026-…, KB…)" aria-label="buscar item">
<label>Por página <select name="por">{{range .Pers}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="{{if eq .View "correcao"}}/painel/patch?ver=correcao{{else}}/painel/patch{{end}}">Limpar</a>{{end}}</form>
{{if .WholeFix}}<form class="aviso atencao" method="post" action="/painel/patch/correcao"><input type="hidden" name="fix" value="{{.WholeFix}}">Os {{.WholeFixN}} itens aguardando deste filtro são CVEs da mesma correção, <code>{{.WholeFix}}</code>. <button type="submit">Conferir a correção inteira</button></form>{{end}}
{{if eq .View "correcao"}}{{template "patch-correcoes" .}}{{else}}{{if .Rows}}{{if .CanBatch}}<form method="post" action="/painel/patch/aprovar-lote">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Q}}<input type="hidden" name="q" value="{{.Q}}">{{end}}{{if gt .Page 1}}<input type="hidden" name="pagina" value="{{.Page}}">{{end}}{{template "patch-filtro-oculto" .}}{{end}}
<div class="tabela"><table>
<thead><tr>{{if .CanBatch}}<th class="col-lote">Lote</th>{{end}}<th>Item</th><th>Tipo</th><th>Corrigido em</th><th>Severidade</th><th>Explorada</th><th>Sistemas</th><th>Catálogo</th><th>Aprovação</th></tr></thead>
<tbody>{{range .Rows}}<tr>{{if $.CanBatch}}<td class="col-lote">{{if .Sel}}<input type="checkbox" name="sel" value="{{.Sel}}" aria-label="marcar {{.ID}}">{{end}}</td>{{end}}<td><a href="{{.Href}}"><code>{{.ID}}</code></a>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}</td>
<td>{{.Kind}}</td><td><code>{{.FixedIn}}</code></td><td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td>
<td class="num"><a href="{{.Href}}">{{.Machines}}</a></td><td>{{.Catalogs}}</td>
<td>{{if .ApprovedBy}}<span class="estado em_dia">aprovado</span> <span class="muted">{{.ApprovedBy}} · {{.ApprovedAt}}</span>{{else}}<span class="estado reboot_pendente">aguardando</span>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if .CanBatch}}<div class="lote"><button type="submit">Conferir marcados</button>
<button class="sec" type="submit" name="filtro" value="1">Conferir todos os {{.FilterWaiting}} aguardando{{if .HasFilter}} deste filtro{{end}}</button>
<span class="muted">Nada é aprovado antes da conferência. Todos do filtro vão num lote só, sem limite.</span></div></form>{{end}}
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhum item{{if .HasFilter}} com esse filtro{{else}} pendente no parque{{end}}.</p>{{end}}{{end}}
</div>{{end}}
{{template "fim"}}{{end}}

{{define "patch-filtro-oculto"}}{{if .OS}}<input type="hidden" name="sistema" value="{{.OS}}">{{end}}{{if .Sev}}<input type="hidden" name="severidade" value="{{.Sev}}">{{end}}{{if ne .Per 25}}<input type="hidden" name="por" value="{{.Per}}">{{end}}{{end}}

{{define "patch-correcoes"}}{{if .FixRows}}{{if .CanBatch}}<form method="post" action="/painel/patch/aprovar-lote"><input type="hidden" name="ver" value="correcao">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Q}}<input type="hidden" name="q" value="{{.Q}}">{{end}}{{if gt .Page 1}}<input type="hidden" name="pagina" value="{{.Page}}">{{end}}{{template "patch-filtro-oculto" .}}{{end}}
<div class="tabela"><table>
<thead><tr>{{if .CanBatch}}<th class="col-lote">Lote</th>{{end}}<th>Correção</th><th>Sistema</th><th>Resolve</th><th>Severidade</th><th>Explorada</th><th>Sistemas afetados</th><th>Aprovação</th></tr></thead>
<tbody>{{range .FixRows}}<tr>{{if $.CanBatch}}<td class="col-lote">{{if .Sel}}<input type="checkbox" name="sel" value="{{.Sel}}" aria-label="marcar {{.Label}}">{{end}}</td>{{end}}
<td><a href="{{.Href}}"><code>{{.Label}}</code></a>{{if .Detail}}<div class="muted nota">{{.Detail}}</div>{{end}}</td><td>{{.System}}</td><td class="num">{{.Items}} {{.Unit}}</td>
<td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td class="num">{{.Machines}}</td>
<td><span class="estado {{.ApprovalClass}}">{{.Approval}}</span></td></tr>{{end}}</tbody></table></div>
{{if .CanBatch}}<div class="lote"><button type="submit">Conferir marcadas</button>
<button class="sec" type="submit" name="filtro" value="1">Conferir todas as {{.FilterWaiting}} aguardando{{if .HasFilter}} deste filtro{{end}}</button>
<span class="muted">Aprovar uma correção aprova todas as CVEs dela. Nada é aprovado antes da conferência. Todas do filtro vão num lote só, sem limite.</span></div></form>{{end}}
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhuma correção{{if .HasFilter}} com esse filtro{{else}} pendente no parque{{end}}.</p>{{end}}{{end}}

{{define "patch-lote"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Patch</a></p>
<h1>Aprovar em lote <span class="muted">› conferência</span></h1>
{{if .FixMode}}<p class="aviso atencao">{{.Total}} {{if eq .Total 1}}correção será aprovada{{else}}correções serão aprovadas{{end}}: {{.FixItems}} item(ns) aguardando ao todo{{if .Exploited}}, em {{.Exploited}} correção(ões) com exploração conhecida{{end}}. A partir daí, cada item pode entrar em atualização e em campanha.</p>
{{else}}<p class="aviso atencao">{{.Total}} {{if eq .Total 1}}item será aprovado{{else}}itens serão aprovados{{end}} com a correção indicada{{if .Exploited}}, {{.Exploited}} com exploração conhecida{{end}}. A partir daí, cada um pode entrar em atualização e em campanha.</p>{{end}}
{{if .More}}<p class="muted">A tabela mostra {{if .FixMode}}as{{else}}os{{end}} {{len .Rows}} {{if .FixMode}}primeiras{{else}}primeiros{{end}}, pela ordem da lista (exploradas e mais graves primeiro); {{if .FixMode}}as outras{{else}}os outros{{end}} {{.More}} entram no mesmo lote.</p>{{end}}
{{if .Skipped}}<p class="muted">{{.Skipped}} marcado(s) ficaram de fora: já aprovados, ou não estão mais pendentes.</p>{{end}}
<form method="post" action="/painel/patch/aprovar-lote">
<input type="hidden" name="confirmar" value="1">{{if .Status}}<input type="hidden" name="status" value="{{.Status}}">{{end}}{{if .Q}}<input type="hidden" name="q" value="{{.Q}}">{{end}}{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}
{{if .Digest}}<input type="hidden" name="filtro" value="1"><input type="hidden" name="resumo" value="{{.Digest}}">{{end}}
<div class="card"><div class="card-cab"><h2>{{if .FixMode}}Correções do lote{{else}}Itens do lote{{end}}</h2><span class="muted">avaliação de agora</span></div>
<div class="tabela"><table>
<thead><tr><th>{{if .FixMode}}Correção{{else}}Item{{end}}</th><th>Tipo</th><th>Corrigido em</th><th>Severidade</th><th>Explorada</th><th>Sistemas</th></tr></thead>
<tbody>{{range .Rows}}<tr><td>{{if not $.Digest}}<input type="hidden" name="sel" value="{{.Sel}}">{{end}}<code>{{.ID}}</code></td><td>{{.Kind}}</td><td><code>{{.FixedIn}}</code></td>
<td>{{.Severity}}</td><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td class="num">{{.Machines}}</td></tr>{{end}}</tbody></table></div>
</div>
<div class="card"><div class="card-cab"><h2>Aprovação</h2></div>
<div class="acao"><input name="nota" value="{{.Note}}" maxlength="{{.MaxNote}}" placeholder="Observação para todos os itens (opcional), ex.: testado no grupo de implantação piloto em 10/10" aria-label="observação"></div>
<div class="lote"><button type="submit">Aprovar {{.Total}} {{if .FixMode}}{{if eq .Total 1}}correção{{else}}correções{{end}}{{else}}{{if eq .Total 1}}item{{else}}itens{{end}}{{end}}</button><a class="btn" href="{{.Back}}">Voltar sem aprovar</a>
<span class="muted">{{if .Digest}}No clique, o servidor refaz o filtro e só aprova se a lista for a mesma desta conferência; o que outra pessoa aprovou antes fica com a aprovação dela.{{else}}O servidor confere cada item de novo no clique: o que outra pessoa aprovou antes fica com a aprovação dela.{{end}}</span></div>
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
<div class="acao"><input name="nota" value="{{.Note}}" maxlength="{{.MaxNote}}" placeholder="Observação para todas (opcional), ex.: KB testado no grupo de implantação piloto em 10/10" aria-label="observação"></div>
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
<input name="nota" maxlength="{{.MaxNote}}" placeholder="Observação (opcional), ex.: testado no grupo de implantação piloto"><button type="submit">Aprovar</button></form>
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
<p><a href="/painel/inventario">← Inventário</a> · <a href="/painel/parque">Parque</a></p>
<h1>{{.Title}} <span class="estado {{.D.State}}">{{.Label}}</span></h1>
{{if .Admin}}<div class="descoberta-acoes"><a class="btn" href="/painel/maquinas/{{.D.MachineID}}/mesclar">Mesclar registro duplicado</a><a class="btn sec" href="/painel/maquinas/{{.D.MachineID}}/historico">Histórico e registros incorporados</a></div>{{end}}
{{if and .Admin .MergedRecords}}<p class="aviso atencao">{{.MergedRecords}} registro(s) anterior(es) incorporado(s), com {{.MergedBIOS}} senha(s) de BIOS preservada(s). <a href="/painel/maquinas/{{.D.MachineID}}/historico">Consultar histórico e senhas anteriores</a>.</p>{{end}}
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
{{if .Host.SignersKnown}}<dt>Instaladores aceitos</dt>{{if .Host.Signers}}<dd>só os assinados por {{.Host.Signers}}</dd>{{else}}<dd class="nivel-atencao">qualquer assinatura (sem <code>-allowed-signers</code> nesta máquina)</dd>{{end}}{{end}}
{{if .Host.Has}}<dt>Chave de cifragem</dt>{{if .Host.BoxKeyID}}<dd><code>{{.Host.BoxKeyID}}</code> <span class="muted">(segredos para esta máquina são fechados com ela)</span></dd>{{else}}<dd class="muted">não informada (agente anterior à v0.18.2)</dd>{{end}}{{end}}
<dt>Canal</dt><dd>{{.Channel}}</dd>{{if .Network}}
<dt>Rede</dt><dd>{{.Network}}</dd>{{end}}{{if .RealTime}}
<dt>Tempo real</dt><dd>{{.RealTime}}</dd>{{end}}
{{if .Ring}}<dt>Grupo de implantação</dt><dd><a href="/painel/grupos-implantacao/{{.RingN}}">{{.Ring}}</a>{{if .Admin}} <form class="inline" method="post" action="/painel/maquinas/{{.D.MachineID}}/anel"><select name="anel" aria-label="grupo de implantação novo">{{range .Rings}}<option value="{{.}}" {{if eq . $.RingN}}selected{{end}}>{{.}}</option>{{end}}</select> <button class="sec" type="submit">Mudar de grupo de implantação</button></form>{{end}}</dd>
<dt>Janela</dt><dd>{{if .Window}}{{.Window}}{{if .NextWindow}} · {{.NextWindow}}{{end}}{{else}}— (jobs com janela não podem ser criados para este grupo de implantação){{end}}</dd>{{end}}
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
<p class="muted">A máquina sai do parque, do compliance e do painel, e a credencial dela deixa de valer. Nada é apagado; dá para restaurar em Aposentadas.</p>
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
{{if .FWParams}}<div class="card"><div class="card-cab"><h2>Firmware</h2><span class="muted">só leitura</span></div>
<dl class="dados">{{range .FWParams}}<dt>{{.Label}}</dt><dd{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Value}}</dd>{{end}}</dl>
</div>{{end}}
{{with .Wake}}<div class="card" id="wol"><div class="card-cab"><h2>Ligar pela rede (Wake on LAN)</h2><span class="muted">só leitura</span></div>
<dl class="dados"><dt>Situação</dt><dd class="nivel-{{.Level}}">{{.State}}</dd>
{{if .Blockers}}<dt>Impede</dt><dd><ul class="usuarios">{{range .Blockers}}<li>{{.}}</li>{{end}}</ul></dd>{{end}}
{{if .Notes}}<dt>Atenção</dt><dd><ul class="usuarios">{{range .Notes}}<li>{{.}}</li>{{end}}</ul></dd>{{end}}
<dt>Fast Startup</dt><dd>{{.FastStartup}}</dd>
<dt>BitLocker</dt><dd>{{.BitLocker}}</dd>
{{if .Settings}}<dt>Na BIOS</dt><dd>{{range $i, $s := .Settings}}{{if $i}}<br>{{end}}{{$s}}{{end}}</dd>{{end}}
</dl>
{{if .Adapters}}<div class="tabela"><table><thead><tr><th>Placa</th><th>MAC</th><th>Cabo</th><th>Magic Packet</th><th>Pode ligar o computador</th></tr></thead>
<tbody>{{range .Adapters}}<tr><td>{{.Name}}</td><td><code>{{.MAC}}</code></td><td>{{.Wired}}</td><td>{{.Magic}}</td><td>{{.DeviceWake}}</td></tr>{{end}}</tbody></table></div>{{end}}
</div>{{end}}
{{with .WakeOn}}<div class="card" id="ligar"><div class="card-cab"><h2>Ligar agora</h2><span class="muted">pacote mágico pela mesma sub-rede</span></div>
<p class="muted">O pacote mágico não passa pelo roteador: o patchd pede a até 3 máquinas ligadas da mesma sub-rede (com check-in nas últimas 2 horas) que o mandem, por broadcast local, para {{if .MACs}}<code>{{.MACs}}</code>{{else}}as placas com cabo desta máquina{{end}}. O pedido sai no próximo check-in de um deles (até {{$.CheckinWait}}).</p>
{{if .Pending}}<p class="aviso atencao">{{.Pending}}.</p>
{{else if .CanRequest}}<form method="post" action="/painel/maquinas/{{$.D.MachineID}}/ligar"><button type="submit">Ligar pela rede</button></form>
{{else if not .MACs}}<p class="muted">Sem MAC conhecido de uma placa com cabo: o agente ainda não mandou a rede desta máquina.</p>{{end}}
{{if .Requests}}<div class="tabela"><table><thead><tr><th>Pedido</th><th>Por</th><th>Sub-rede</th><th>Repetidores</th><th>Pacote</th><th>Máquina</th></tr></thead>
<tbody>{{range .Requests}}<tr><td class="nowrap">{{.When}}</td><td>{{.By}}</td><td><code>{{.Network}}</code></td><td>{{.Relays}}</td><td>{{.Sent}}</td><td{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Answer}}</td></tr>{{end}}</tbody></table></div>{{end}}
</div>{{end}}
{{with .BIOS}}<div class="card" id="bios"><div class="card-cab"><h2>Senha da BIOS</h2>{{if .On}}<span class="muted">chave de guarda <code>{{.KeyID}}</code></span>{{end}}</div>
{{if not .On}}<p class="muted">Guarda desligada: o servidor está sem a chave de guarda (<code>PATCHD_BIOS_ESCROW_KEY_FILE</code>; crie com <code>patchd-server bios keygen</code>).</p>{{end}}
{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>#</th><th>Estado</th><th>Sorteada</th><th>Por</th><th>Revelações</th>{{if and .Admin .On}}<th>Ações</th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td class="num">{{.ID}}</td><td><span{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.State}}</span>{{if .Result}}<br><span class="muted">{{.Result}}</span>{{end}}</td><td>{{.Created}}</td><td>{{.By}}</td><td>{{.Reveals}}</td>
{{if and $.BIOS.Admin $.BIOS.On}}<td>{{if .CanWrite}}<form class="inline" method="post" action="/painel/maquinas/{{$.D.MachineID}}/bios/{{.ID}}/gravar">{{if $.BIOS.NeedCurrent}}{{if $.BIOS.Legacy}}<input type="password" name="senha_atual" maxlength="{{$.BIOS.MaxCurrent}}" autocomplete="off" placeholder="em branco: a do Registro (.bat)" aria-label="senha atual da BIOS (em branco: o agente usa a que o .bat deixou no Registro desta máquina)">{{else if $.BIOS.FleetLabel}}<input type="password" name="senha_atual" maxlength="{{$.BIOS.MaxCurrent}}" autocomplete="off" placeholder="em branco: a padrão da {{$.BIOS.FleetLabel}}" aria-label="senha atual da BIOS (em branco: a senha padrão da {{$.BIOS.FleetLabel}})">{{else}}<input type="password" name="senha_atual" required maxlength="{{$.BIOS.MaxCurrent}}" autocomplete="off" placeholder="senha atual da BIOS" aria-label="senha atual da BIOS (a padrão do parque)">{{end}} {{end}}<button type="submit">Gravar na BIOS</button></form>{{end}}
{{if .CanResolve}}<form class="inline" method="post" action="/painel/maquinas/{{$.D.MachineID}}/bios/{{.ID}}/resolver"><span class="muted">conferida na BIOS:</span> <button class="sec" type="submit" name="valor" value="nova">A nova vale</button> <button class="sec" type="submit" name="valor" value="anterior">A anterior vale</button></form>{{end}}
{{if .CanReveal}}<form class="inline" method="post" action="/painel/maquinas/{{$.D.MachineID}}/bios/{{.ID}}/revelar"><input name="motivo" required minlength="{{$.BIOS.MinReason}}" maxlength="{{$.BIOS.MaxReason}}" placeholder="motivo (chamado, OS...)" aria-label="motivo da revelação da senha #{{.ID}}"> <button class="sec" type="submit">Revelar</button></form>{{else if .OtherKey}}<span class="muted">outra chave de guarda (<code>{{.OtherKey}}</code>)</span>{{else if not .CanWrite}}—{{end}}</td>{{end}}</tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma senha guardada para esta máquina.</p>{{end}}
{{if .CanPrepare}}<form method="post" action="/painel/maquinas/{{$.D.MachineID}}/bios/preparar"><p class="muted">Sorteia uma senha de 12 caracteres e a guarda cifrada. Nada muda na máquina até a gravação.</p><button class="sec" type="submit">Preparar senha nova</button></form>
{{else if and .Admin .On .Pending}}<p class="muted">A senha #{{.Pending}} está pendente: é ela que a próxima gravação usa.</p>{{end}}
{{if and .Admin .On .Pending .Blocker}}<p class="aviso atencao">Gravação indisponível: {{.Blocker}}.</p>{{else if and .Admin .On .NeedCurrent .Legacy}}<p class="muted">A BIOS já tem senha e a guarda não tem a atual, mas o Registro desta máquina tem a que o <code>.bat</code> deixou: com o campo em branco, o agente a lê na própria máquina (ela não vem para o servidor), troca pela senha da guarda e apaga a antiga do Registro. Se a BIOS recusar, o agente não repete; digite então a senha em uso.</p>{{else if and .Admin .On .NeedCurrent .FleetLabel}}<p class="muted">A BIOS já tem senha e a guarda não tem a atual: a primeira troca usa a senha padrão da {{.FleetLabel}} (Configuração › BIOS). Se esta máquina tiver outra, digite-a no campo.</p>{{else if and .Admin .On .NeedCurrent}}<p class="muted">A BIOS já tem senha e a guarda não tem a atual: na primeira troca, digite a senha em uso (a padrão do parque), ou cadastre a senha padrão do fabricante em Configuração › BIOS. Ela vai cifrada para a máquina e não fica guardada.</p>{{end}}
</div>{{end}}
<div class="card"><div class="card-cab"><h2>Rede e discos</h2>{{if .Host.Has}}<span class="muted">coletado {{.Host.Age}}</span>{{end}}</div>
{{if .Host.Has}}<dl class="dados">
<dt>IP principal</dt><dd>{{.Host.Primary}}</dd>
<dt>Check-in vindo de</dt><dd>{{.Host.Addr}}{{if .Host.AddrNote}} <span class="muted">· {{.Host.AddrNote}}</span>{{end}}</dd>
</dl>
{{if .Host.Disks}}<h3>Discos</h3><div class="tabela"><table>
<thead><tr><th>Volume</th><th>Sistema de arquivos</th><th>Tamanho</th><th>Livre</th><th>Ocupado</th></tr></thead>
<tbody>{{range .Host.Disks}}<tr><td><code>{{.Mount}}</code>{{if .Device}} <span class="muted">{{.Device}}</span>{{end}}{{if .Model}} <span class="muted">{{.Model}}{{if .Bus}} · {{.Bus}}{{end}}</span>{{end}}</td><td>{{.FS}}</td><td class="num">{{.Total}}</td><td class="num">{{.Free}}</td>
<td><span class="uso {{.Level}}" role="img" aria-label="{{.Pct}}% ocupado"><span class="w{{.Pct}}"></span></span> {{.Pct}}%</td></tr>{{end}}</tbody></table></div>{{end}}
{{if .Host.Ifs}}<h3>Interfaces</h3><div class="tabela"><table>
<thead><tr><th>Interface</th><th>Estado</th><th>MAC</th><th>IPv4</th><th>IPv6</th></tr></thead>
<tbody>{{range .Host.Ifs}}<tr><td>{{.Name}}</td><td>{{if .Up}}ativa{{else}}<span class="muted">inativa</span>{{end}}</td>
<td><code>{{.MAC}}</code>{{if .LocalMAC}} <span class="muted" title="MAC administrado localmente: bridge, VPN, container ou Wi-Fi com endereço aleatório">local</span>{{end}}</td><td>{{.IPv4}}</td><td>{{.IPv6}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{range .Host.Errors}}<p class="aviso atencao">{{.}}</p>{{end}}
{{else}}<dl class="dados"><dt>Check-in vindo de</dt><dd>{{.Host.Addr}}</dd></dl>
<p class="muted">O agente desta máquina ainda não manda rede e discos: a coleta chegou na Aula 12.1. Atualize o agente.</p>{{end}}
</div>
{{end}}

{{if eq .Tab "bios"}}{{with .BIOSConfig}}
{{if .Summary}}<div class="card" id="bios-resumo"><div class="card-cab"><h2>Resumo da BIOS</h2><span class="muted">os mesmos itens para qualquer marca e modelo{{if .Vendor}} · {{.Vendor}}{{end}}</span></div>
<div class="tabela"><table><thead><tr><th>Item</th><th>Nesta máquina</th><th>Padrão da BIOS</th></tr></thead>
<tbody>{{range .Summary}}<tr><td><b>{{.Item}}</b>{{if .Help}}<br><small class="muted">{{.Help}}</small>{{end}}</td>
<td><span{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.State}}</span>{{if .Detail}}<br><small class="muted">{{.Detail}}</small>{{end}}</td>
<td>{{if .Standard}}<span class="nivel-{{.StdLevel}}">{{.Standard}}</span>{{else}}<span class="muted">—</span>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if $.Admin}}<p class="muted">Para deixar o parque inteiro no padrão (senha, ligar pela rede e bloquear o pendrive), use <a href="/painel/bios/padrao">Configuração › BIOS › Padrão da BIOS</a>.</p>{{end}}
</div>{{end}}
<div class="card" id="bios-config"><div class="card-cab"><h2>Configurações da BIOS</h2>{{if .Has}}<span class="muted">{{.Total}} lidas · {{.EditableCount}} que o patchd pode mudar</span>{{end}}</div>
{{if not .Has}}<p class="muted">{{.Reason}}</p>
{{else}}
{{if and .Admin .EditableCount (not .CanEdit)}}<p class="aviso atencao">Não dá para mudar agora: {{.Blocker}}.</p>{{end}}
{{if .Groups}}{{if .CanEdit}}<form method="post" action="/painel/maquinas/{{$.D.MachineID}}/bios/configurar">{{end}}
{{range .Groups}}<h3>{{.Title}}</h3><div class="tabela"><table class="bios-cfg">
<thead><tr><th class="c-cfg">Configuração</th><th class="c-val">Como está</th>{{if $.BIOSConfig.CanEdit}}<th class="c-new">Mudar para</th>{{end}}<th>Atenção</th></tr></thead>
<tbody>{{range $row := .Rows}}<tr><td><b>{{$row.Title}}</b>{{if $row.Help}}<br><small class="muted">{{$row.Help}}</small>{{end}}{{if $row.Plain}}<br><small class="muted">na BIOS: {{$row.Name}}</small>{{else if $row.Path}}<br><small class="muted">{{$row.Path}}</small>{{end}}</td>
<td>{{$row.ValueText}}{{if ne $row.ValueText $row.Value}}<br><small class="muted">{{$row.Value}}</small>{{end}}</td>
{{if $.BIOSConfig.CanEdit}}<td><input type="hidden" name="n{{$row.Index}}" value="{{$row.Name}}"><select name="v{{$row.Index}}" aria-label="mudar {{$row.Title}} para">{{range $row.Choices}}<option value="{{.Value}}"{{if eq .Value $row.Value}} selected{{end}}>{{.Label}}</option>{{end}}</select></td>{{end}}
<td>{{if $row.Risk}}<span class="nivel-atencao">cuidado</span>{{if $row.Note}}: {{$row.Note}}{{end}}{{else}}<span class="muted">—</span>{{end}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{if .CanEdit}}<div class="lote">
<input name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="Motivo (chamado, OS...)" aria-label="motivo da alteração">
{{if .HasRisk}}<label><input type="checkbox" name="confirmo" value="1"> confirmo as mudanças marcadas com "cuidado"</label>{{end}}
<button type="submit">Pedir alteração</button>
<span class="muted">Só vai o que você mudou (até 20 por vez). A máquina confere de novo na própria BIOS, pausa o BitLocker até o próximo reinício (se ele estiver ligado) e grava com a senha guardada pelo patchd. As mudanças valem quando o computador reiniciar; o patchd não reinicia sozinho.</span>
</div></form>{{end}}
{{else}}<p class="muted">Nenhuma configuração desta BIOS está entre as que o patchd pode mudar (ligar e desligar, inicialização, segurança, portas e dispositivos), ou estão todas bloqueadas na própria BIOS.</p>{{end}}
{{if .Others}}<details><summary>As outras {{len .Others}} configurações (só para consulta)</summary><div class="tabela"><table>
<thead><tr><th>Configuração</th><th>Como está</th><th>Opções</th></tr></thead>
<tbody>{{range .Others}}<tr><td>{{if .Plain}}<b>{{.Title}}</b><br><small class="muted">na BIOS: {{.Name}}</small>{{else}}{{.Name}}{{if .Path}}<br><small class="muted">{{.Path}}</small>{{end}}{{end}}{{if .ReadOnly}}<br><small class="muted">bloqueada na própria BIOS</small>{{end}}</td><td>{{.ValueText}}</td><td class="muted">{{.Possible}}</td></tr>{{end}}</tbody></table></div></details>{{end}}
{{end}}
</div>
{{if .Recent}}<div class="card"><div class="card-cab"><h2>Alterações pedidas</h2><a href="/painel/maquinas/{{$.D.MachineID}}?aba=tarefas">todas as tarefas</a></div><div class="tabela"><table>
<thead><tr><th>Tarefa</th><th>Situação</th><th>Pedido</th><th>Por</th><th>O que foi pedido</th><th>Resultado</th></tr></thead>
<tbody>{{range .Recent}}<tr><td class="num">{{.ID}}</td><td><span class="estado {{.State}}">{{.StateLabel}}</span></td><td>{{.Created}}</td><td>{{.By}}</td><td>{{range $i, $c := .Changes}}{{if $i}}<br>{{end}}{{$c}}{{end}}</td><td class="muted">{{.Detail}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{end}}{{end}}

{{if eq .Tab "desempenho"}}{{with .Perf}}
<div class="card"><div class="card-cab"><h2>Desempenho</h2>{{if .At}}<span class="muted">última medida {{.At}}</span>{{end}}</div>
{{if not .Has}}<p class="muted">Nenhuma medida desta máquina ainda: o agente manda o desempenho a partir desta versão (atualize o agente), no check-in seguinte.</p>
{{else}}
<nav class="abas" aria-label="período">{{range .Periods}}<a href="{{.Href}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<div class="graficos">{{range .Charts}}{{template "perf-grafico" .}}{{end}}</div>
<p class="muted legenda-geral">{{if .Hourly}}Média por hora; a dica mostra também o pico do intervalo.{{else}}Uma medida por minuto.{{end}} Passe o mouse sobre um gráfico para ver os valores. A linha tracejada vermelha é o limite do alerta; a faixa vermelha, um alerta aberto.</p>
{{if .Table}}<details class="tabela-grafico"><summary>Ver os valores em tabela</summary><div class="tabela"><table>
<thead><tr><th>Intervalo</th><th>CPU (média)</th><th>Memória (média)</th><th>Disco (menor livre)</th></tr></thead>
<tbody>{{range .Table}}<tr><td class="nowrap">{{.When}}</td><td class="num">{{.CPU}}</td><td class="num">{{.Mem}}</td><td class="num">{{.Disk}}</td></tr>{{end}}</tbody></table></div></details>{{end}}
{{end}}
</div>
<div class="card"><div class="card-cab"><h2>Alertas desta máquina</h2><a href="/painel/alertas">todos os alertas</a></div>
{{if .Alerts}}<div class="tabela"><table><thead><tr><th>Medida</th><th>Aberto em</th><th>Duração</th><th>Pico</th><th>Último</th><th>Estado</th></tr></thead>
<tbody>{{range .Alerts}}<tr><td>{{.Label}}</td><td>{{.Opened}}</td><td>{{.Duration}}</td><td class="num">{{.Peak}}</td><td class="num">{{.Last}}</td><td><span class="nivel-{{.Level}}">{{.State}}</span>{{if .Closed}}<br><span class="muted">{{.Closed}}</span>{{end}}{{if .Ack}}<br><span class="muted">{{.Ack}}</span>{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum alerta nos últimos 90 dias.</p>{{end}}
<p class="muted">Abre com: {{.Policy}}.</p></div>
{{end}}{{end}}

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
{{if .Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do grupo de implantação</label>{{end}}
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
<tbody>{{range .Rows}}<tr><td><code>KB{{.KB}}</code>{{if and $.Admin .Approved}} <a class="muted" href="{{.CampaignHref}}">campanha por grupo de implantação</a>{{end}}</td><td>{{.FixedIn}}</td><td class="num">{{.Approved}}{{if .Exploit}} <span class="expl">{{.Exploit}} explorada(s)</span>{{end}}</td>
<td class="num">{{if .Waiting}}<a href="{{.WaitingHref}}">{{.Waiting}}</a>{{else}}0{{end}}</td>
<td><span class="estado {{.Class}}">{{.Label}}</span>{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma correção do Windows pendente nesta máquina na avaliação de agora.</p>{{end}}
{{if .Open}}<p class="muted">Instalação em aberto: <a href="/painel/maquinas/{{$.D.MachineID}}?aba=tarefas">job {{.Open}}</a>.</p>{{end}}
{{if .CanInstall}}<form class="lote" method="post" action="/painel/maquinas/{{$.D.MachineID}}/windows/instalar">
<label><input type="radio" name="quando" value="agora" checked> agora</label>
{{if $.Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do grupo de implantação</label>{{end}}
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
{{if .Window}}<label><input type="radio" name="quando" value="janela"> na próxima janela do grupo de implantação</label>{{end}}
<label><input type="radio" name="quando" value="horario"> em horário marcado:</label>
<label>a partir de <input type="datetime-local" name="inicio" value="{{.RebootAt}}" aria-label="início do reinício"></label>
<label>vale por <select name="validade" aria-label="por quanto tempo o pedido vale">{{range .RebootValid}}<option value="{{.Minutes}}" {{if eq .Minutes 180}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<label><input type="checkbox" name="ligar" value="1"> ligar pela rede 10 min antes</label>
<button type="submit">Reiniciar com aviso</button>
<span class="muted">Horário no fuso {{.PanelTZ}}. A máquina reinicia no primeiro check-in dentro desse intervalo; se ficar desligada o tempo todo, o pedido vence sem reiniciar. Em horário marcado não se pede a confirmação de sessões ativas: o horário é o combinado.</span></form></section>{{end}}
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
{{if eq .Tab "terminal"}}{{with .Terminal}}
<div class="card" id="terminal"><div class="card-cab"><h2>Terminal do administrador</h2><span class="acoes"><a href="/painel/terminal?maquina={{.MachineID}}">histórico completo</a></span></div>
{{if .Why}}<p class="aviso atencao">{{.Why}}</p>{{end}}
<p class="muted">Um comando por vez, com a conta do agente (SYSTEM no Windows, root no Linux), sem teclado: o que pedir uma resposta fica sem ela. Cada comando começa na raiz do disco; para mudar de pasta, use <code>cd PASTA; comando</code> na mesma linha. <b>Tudo fica registrado</b> (quem, quando, o texto, o código de saída e a saída) por 365 dias: <b>não digite senha</b> na linha de comando.</p>
{{if .Rows}}<div class="terminal" role="log" aria-label="comandos e saídas">{{range .Rows}}<div class="term-item"><div class="term-cab"><span class="term-prompt">{{.Prompt}}</span> <span class="term-cmd">{{.Command}}</span></div>
<div class="term-meta">{{.User}} · {{.When}} · <span class="job {{.Class}}">{{.State}}</span>{{if .Note}} · {{.Note}}{{end}}</div>
{{if .HasOutput}}<pre class="term-saida">{{.Output}}</pre>{{else if not .Pending}}<pre class="term-saida vazia">(sem saída)</pre>{{end}}</div>{{end}}</div>
{{if .Refresh}}<p class="muted">Esperando a máquina: a página se atualiza sozinha a cada 3 segundos.</p>{{else if .Pending}}<p class="muted">A máquina ainda não respondeu (desligada, ou sem o canal em tempo real): <a href="/painel/maquinas/{{.MachineID}}?aba=terminal#terminal">atualizar</a>. O comando expira em 10 minutos.</p>{{end}}
{{else}}<p class="muted">Nenhum comando nesta máquina ainda.</p>{{end}}
{{if not .Why}}{{if .Session}}
<form class="term-form" method="post" action="/painel/maquinas/{{.MachineID}}/terminal/comando">
<select name="shell" aria-label="interpretador">{{range .Shells}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<input type="text" name="comando" maxlength="{{.MaxLen}}" required autofocus autocomplete="off" spellcheck="false" placeholder="comando" aria-label="comando">
<select name="tempo" aria-label="tempo máximo">{{range .Timeouts}}<option value="{{.}}" {{if eq . $.Terminal.Timeout}}selected{{end}}>até {{.}} s</option>{{end}}</select>
<button type="submit">Executar</button></form>
<form class="lote" method="post" action="/painel/maquinas/{{.MachineID}}/terminal/fechar"><span class="muted">Sessão aberta até {{.Until}} (cada comando renova por 15 min). Motivo: {{.Reason}}</span> <button class="sec" type="submit">Fechar a sessão</button></form>
{{else}}
<form class="formulario" method="post" action="/painel/maquinas/{{.MachineID}}/terminal/abrir">
<div class="campo"><label for="motivo">Motivo para abrir o terminal nesta máquina</label><input id="motivo" name="motivo" type="text" required minlength="{{.MinR}}" maxlength="{{.MaxR}}" placeholder="ex.: chamado 12345, conferir o serviço de impressão">
<span class="muted">Obrigatório, de {{.MinR}} a {{.MaxR}} caracteres. Fica na auditoria, junto de cada comando, e a lista de notificações recebe um e-mail. A sessão vale 15 minutos sem comando.</span></div>
<div class="acoes"><button type="submit">Abrir o terminal</button></div></form>
{{end}}{{end}}</div>
{{end}}{{end}}
{{template "fim"}}{{end}}

{{define "jobs-tabela"}}<div class="tabela"><table>
<thead><tr><th>Job</th>{{if not .MachineID}}<th>Máquina</th><th>Nome</th>{{end}}<th>Tipo</th><th>Estado</th><th>Janela</th><th>Criado</th><th>Por</th><th>Entregue</th><th>Encerrado</th><th>Detalhe</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td class="num">{{.ID}}</td>
{{if not $.MachineID}}<td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td>{{end}}
<td>{{.Type}}</td><td><span class="job {{.State}}">{{.StateLabel}}</span></td><td>{{.Window}}</td><td>{{.Created}}</td><td>{{.By}}</td>
<td>{{.Delivered}}</td><td>{{.Finished}}</td><td class="detalhe">{{template "leia-mais" (leia .Detail)}}</td>
{{if $.Admin}}<td>{{if .CanCancel}}<form method="post" action="/painel/jobs/{{.ID}}/cancelar">{{if $.MachineID}}<input type="hidden" name="maquina" value="{{$.MachineID}}">{{end}}<button class="perigo" type="submit">Cancelar</button></form>{{end}}</td>{{end}}
</tr>{{end}}</tbody></table></div>{{end}}

{{define "jobs"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Jobs</span></h1>
{{template "automacao-abas" "jobs"}}
<div class="card"><div class="card-cab"><h2>Mais recentes</h2><span class="muted">Os {{.N}} mais recentes do parque. Para pedir uma busca, abra a máquina no Parque.</span></div>
{{if .Jobs.Rows}}{{template "jobs-tabela" .Jobs}}{{else}}<p class="muted">Nenhum job ainda.</p>{{end}}</div>
{{template "fim"}}{{end}}

{{define "campanhas"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Campanhas</span></h1>
{{template "automacao-abas" "campanhas"}}
<p class="muted">O mesmo pedido passando pelos grupos de implantação em ordem: o servidor libera o grupo seguinte quando o atual termina sem falha e o tempo de observação passa. Uma falha pausa a campanha até alguém decidir; um job adiado (programa aberto) ganha nova tentativa a cada hora, até 24.</p>
<div class="card"><div class="card-cab"><h2>{{if .Archived}}Campanhas arquivadas{{else}}Campanhas recentes{{end}}</h2>
<span class="acoes">{{if .Archived}}<a href="/painel/campanhas">← Campanhas</a>{{else}}{{if and .Admin .NFinished}}<form class="inline" method="post" action="/painel/campanhas/arquivar-encerradas"><button class="sec" type="submit" title="tiram da lista as concluídas e as canceladas; nada se perde">Arquivar as encerradas ({{.NFinished}})</button></form>{{end}}{{if .NArchived}} <a href="/painel/campanhas?arquivadas=1">Arquivadas ({{.NArchived}})</a>{{end}}{{end}}</span></div>{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>ID</th><th>Nome</th><th>Tipo</th><th>Alvo</th><th>Grupos de implantação</th><th>Estado</th><th>Detalhe</th><th>Criada</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="num"><a href="/painel/campanhas/{{.ID}}">{{.ID}}</a></td><td><a href="/painel/campanhas/{{.ID}}">{{.Name}}</a></td><td>{{.Type}}</td><td>{{.Sources}}</td><td><code>{{.Rings}}</code></td>
<td><span class="job {{.State}}">{{.Label}}</span></td><td class="detalhe">{{template "leia-mais" (leia .Detail)}}</td><td class="nowrap">{{.Created}} · {{.By}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">{{if .Archived}}Nenhuma campanha arquivada.{{else}}Nenhuma campanha ainda.{{end}}</p>{{end}}</div>
{{if .NoKey}}<p class="muted">Criar campanhas pelo painel exige a chave de jobs no servidor (PATCHD_JOB_SIGNING_KEY_FILE).</p>{{end}}
{{if .CanNew}}<section id="nova"><h2>Nova campanha</h2>
<form class="campanha" method="post" action="/painel/campanhas/nova">
<div class="campo"><label for="nome">Nome</label><input id="nome" name="nome" type="text" required maxlength="100" value="{{.Form.Name}}" placeholder="ex.: curl e bluez, outubro"></div>
<fieldset class="campo tipos"><legend>Tipo</legend>
<label class="opcao"><input type="radio" name="tipo" value="sistema_todas" {{if eq .Form.Type "sistema_todas"}}checked{{end}}><span><b>Todas as atualizações do sistema</b><small>cada máquina recebe tudo o que está pendente nela e aprovado em Patch: pacotes no Linux, KBs no Windows (nunca reinicia)</small></span></label>
{{if .Form.TPOn}}<label class="opcao"><input type="radio" name="tipo" value="terceiros_todos" {{if eq .Form.Type "terceiros_todos"}}checked{{end}}><span><b>Todos os programas de terceiros aprovados (Windows)</b><small>cada programa vai para a versão aprovada mais nova, só onde já está instalado</small></span></label>{{end}}
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
<div class="campo so-fechar"><label for="fechar">Programa aberto na hora</label><select id="fechar" name="fechar"><option value="0" {{if eq .Form.Close 0}}selected{{end}}>adiar e tentar de novo quando fechar</option>{{range .Form.Delays}}<option value="{{.}}" {{if eq . $.Form.Close}}selected{{end}}>fechar com aviso de {{.}} min</option>{{end}}</select>
<small class="muted">fechar só em horário fixo (de madrugada) ou na janela do grupo; quem estiver na sessão vê o aviso, e o programa fecha à força se não fechar sozinho</small></div>
<fieldset class="campo so-windows"><legend>Fonte do Windows</legend><label class="opcao"><input type="checkbox" name="microsoft" value="1" {{if .Form.Microsoft}}checked{{end}}><span><b>Se a fonte da política falhar ou não oferecer o KB, instalar direto da Microsoft</b>
<small>só o que está aprovado em Patch; muda de onde o arquivo vem{{if not .Form.MicrosoftOn}} · hoje sem efeito: a fonte reserva está desligada em Configuração › Parâmetros{{end}}</small></span></label></fieldset>
<fieldset class="campo aneis"><legend>Grupos de implantação <small class="muted">em ordem, do menor para o maior</small></legend>
<div class="chips">{{range .Form.Rings}}{{if or .Machines .Checked}}<label class="anel"><input type="checkbox" name="anel" value="{{.Ring}}" {{if .Checked}}checked{{end}}> {{.Label}} <span class="muted">· {{.Machines}} máq.{{if .Window}} · janela {{.Window}}{{end}}</span></label>{{end}}{{end}}</div>
{{$vazios := 0}}{{range .Form.Rings}}{{if not (or .Machines .Checked)}}{{$vazios = 1}}{{end}}{{end}}
{{if $vazios}}<details class="vazios"><summary>Grupos de implantação sem máquinas</summary><div class="chips">{{range .Form.Rings}}{{if not (or .Machines .Checked)}}<label class="anel"><input type="checkbox" name="anel" value="{{.Ring}}"> {{.Label}} <span class="muted">· {{.Machines}} máq.{{if .Window}} · janela {{.Window}}{{end}}</span></label>{{end}}{{end}}</div></details>{{end}}
</fieldset>
<div class="campo"><label for="reenvios">Reenvios automáticos das falhas</label><select id="reenvios" name="reenvios">{{range $n := .Form.Resends}}<option value="{{$n}}" {{if eq $n $.Form.Resend}}selected{{end}}>{{if eq $n 0}}nenhum: a primeira falha pausa a campanha{{else}}até {{$n}} vez(es) por máquina{{end}}</option>{{end}}</select>
<small class="muted">o job que expirou (máquina desligada, fora da rede) vai de novo na hora; o que falhou, depois de 1 h. Recusado não volta sozinho. Só depois disso a falha conta e pausa</small></div>
<div class="campo"><label for="observacao">Observação</label><select id="observacao" name="observacao">{{range .Form.Observe}}<option value="{{.Hours}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<small class="muted">depois de um grupo de implantação terminar sem falha, quanto esperar antes do próximo</small></div>
<fieldset class="campo quando"><legend>Quando</legend>
<label class="opcao"><input type="radio" name="quando" value="prazo" {{if ne .Form.When "janela"}}checked{{end}}><span><b>Já, com prazo de</b>
<select name="prazo" aria-label="prazo">{{range .Form.TTL}}<option value="{{.Hours}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></span></label>
<label class="opcao"><input type="radio" name="quando" value="janela" {{if eq .Form.When "janela"}}checked{{end}}><span><b>Na janela de cada grupo de implantação</b><small>o job de cada máquina espera a janela do grupo dela</small></span></label>
<label class="opcao"><input type="radio" name="quando" value="horario" {{if eq .Form.When "horario"}}checked{{end}}><span><b>Em horário fixo, todo dia</b>
<small class="linha">às <input type="time" name="hora" value="{{.Form.DailyStart}}" aria-label="hora"> por <select name="duracao" aria-label="duração">{{range .Form.Durations}}<option value="{{.Minutes}}" {{if eq .Minutes $.Form.DailyDur}}selected{{end}}>{{.Label}}</option>{{end}}</select></small>
<small>ex.: de madrugada; vale para todos os grupos, sem mexer na janela deles (fuso {{.Form.TZ}})</small></span></label>
</fieldset>
<fieldset class="campo"><legend>Ligar pela rede</legend><label class="opcao"><input type="checkbox" name="ligar" value="1" {{if .Form.Wake}}checked{{end}}><span><b>Ligar as desligadas no início da janela e desligar depois as que o patchd ligou</b>
<small>só com a janela do grupo ou o horário fixo; o agente só desliga sem ninguém conectado. Precisa do Wake on LAN pronto e de um agente ligado em cada sub-rede</small></span></label></fieldset>
<div class="acoes"><button type="submit">Conferir</button> <span class="muted">Nada é criado antes da conferência.</span></div>
</form></section>{{end}}
{{template "fim"}}{{end}}

{{define "campanha-conferir"}}{{template "topo" .}}
<h1>Conferir a campanha</h1>
<div class="card"><dl class="dados">
<dt>Nome</dt><dd>{{.Form.Name}}</dd>
<dt>Tipo</dt><dd>{{.TypeLabel}}{{if .SourcesList}} · {{if eq .Form.Type "windows_update"}}KBs{{else}}fontes{{end}}: {{.SourcesList}}{{end}}{{if .ProgramL}} · {{.ProgramL}}{{end}}{{if .Extra}} · {{.Extra}}{{end}}</dd>
<dt>Quando</dt><dd>{{.When}}</dd>
<dt>Observação</dt><dd>{{.ObserveL}} depois de cada grupo de implantação concluído</dd>
{{if .Form.Wake}}<dt>Ligar pela rede</dt><dd>as desligadas, no início da janela; desligar depois as que o patchd ligou</dd>{{end}}
<dt>Reenvios automáticos</dt><dd>{{if .Form.Resend}}até {{.Form.Resend}} vez(es) por máquina, nas falhas e nos expirados{{else}}nenhum: a primeira falha pausa a campanha{{end}}</dd>
</dl></div>
<div class="card"><div class="card-cab"><h2>Grupos de implantação, na ordem</h2></div><div class="tabela"><table><thead><tr><th>Ordem</th><th>Grupo de implantação</th><th>Máquinas agora</th><th>Janela</th>{{if .ProgramL}}<th>Hoje</th>{{end}}<th></th></tr></thead>
<tbody>{{range $i, $r := .Rings}}<tr><td class="num">{{$i}}</td><td>{{$r.Label}}</td><td class="num">{{$r.Machines}}</td><td>{{$r.Window}}</td>{{if $.ProgramL}}<td>{{$r.Preview}}</td>{{end}}<td class="muted">{{$r.Note}}</td></tr>{{end}}</tbody></table></div></div>
{{range .Warnings}}<p class="aviso atencao">{{.}}</p>{{end}}
<p>Ao criar, o servidor libera o primeiro grupo de implantação no próximo minuto: os jobs vão para máquinas de verdade.</p>
<form class="lote" method="post" action="/painel/campanhas/nova">
<input type="hidden" name="nome" value="{{.Form.Name}}"><input type="hidden" name="tipo" value="{{.Form.Type}}">
<input type="hidden" name="fontes" value="{{.Form.Sources}}"><input type="hidden" name="quando" value="{{.Form.When}}"><input type="hidden" name="hora" value="{{.Form.DailyStart}}"><input type="hidden" name="duracao" value="{{.Form.DailyDur}}">
<input type="hidden" name="programa" value="{{.Form.Program}}"><input type="hidden" name="aviso" value="{{.Form.Delay}}">{{if .Form.Microsoft}}<input type="hidden" name="microsoft" value="1">{{end}}{{if .Form.Close}}<input type="hidden" name="fechar" value="{{.Form.Close}}">{{end}}<input type="hidden" name="reenvios" value="{{.Form.Resend}}">{{if .Form.Wake}}<input type="hidden" name="ligar" value="1">{{end}}
<input type="hidden" name="observacao" value="{{.Form.ObserveH}}"><input type="hidden" name="prazo" value="{{.Form.TTLH}}">
{{range .Form.Chosen}}<input type="hidden" name="anel" value="{{.}}">{{end}}
<button type="submit" name="confirmar" value="1">Criar campanha</button>
<button class="sec" type="submit" name="corrigir" value="1">Corrigir</button>
</form>
{{template "fim"}}{{end}}

{{define "campanha"}}{{template "topo" .}}
<p><a href="/painel/campanhas">← Campanhas</a></p>
<h1>{{.Title}}: {{.C.Name}} <span class="job {{.State}}">{{.StateLabel}}</span>{{if .C.ArchivedAt}} <span class="muted">· arquivada</span>{{end}}</h1>
<div class="card"><dl class="dados">
<dt>Tipo</dt><dd>{{.TypeLabel}}</dd>
<dt>{{if eq .C.Type "thirdparty"}}Programa{{else if eq .C.Type "windows_update"}}KBs{{else if eq .C.Type "reboot"}}Reinício{{else}}Fontes{{end}}</dt><dd>{{.Sources}}</dd>
<dt>Quando</dt><dd>{{.When}}</dd>{{if .CloseL}}
<dt>Programa aberto</dt><dd>{{.CloseL}}</dd>{{end}}{{if .Fallback}}
<dt>Fonte do Windows</dt><dd>{{.Fallback}}</dd>{{end}}
<dt>Observação</dt><dd>{{.Observe}}</dd>
<dt>Reenvios automáticos</dt><dd>{{.ResendL}}</dd>{{if .WakeL}}
<dt>Ligar pela rede</dt><dd>{{.WakeL}}</dd>{{end}}
<dt>Detalhe</dt><dd>{{if .C.Detail}}{{template "leia-mais" (leia .C.Detail)}}{{else}}—{{end}}</dd>
{{if .C.Notes}}<dt>Liberações</dt><dd>{{range .C.Notes}}<div>{{.}}</div>{{end}}</dd>{{end}}
<dt>Criada</dt><dd>{{.Created}} por {{.C.CreatedBy}}</dd>
</dl>
{{if or .CanPause .CanResume .CanCancel}}<div class="lote">
{{if .CanPause}}<form method="post" action="/painel/campanhas/{{.C.ID}}/pausar"><button class="sec" type="submit">Pausar</button></form>{{end}}
{{if .CanResume}}<form method="post" action="/painel/campanhas/{{.C.ID}}/retomar"><button type="submit">Retomar (aceita as falhas do grupo de implantação atual)</button></form>{{end}}
{{if .CanCancel}}<form method="post" action="/painel/campanhas/{{.C.ID}}/cancelar"><button class="perigo" type="submit">Cancelar a campanha</button></form>{{end}}
<span class="muted">Pausar e cancelar não desfazem jobs já entregues; cancelar cancela os pendentes.</span></div>{{end}}
{{if or .CanArchive .CanUnarchive}}<div class="lote">
{{if .CanArchive}}<form method="post" action="/painel/campanhas/{{.C.ID}}/arquivar"><button class="sec" type="submit">Arquivar</button></form> <span class="muted">Sai da lista de campanhas, sem perder nada; dá para desarquivar.</span>{{end}}
{{if .CanUnarchive}}<form method="post" action="/painel/campanhas/{{.C.ID}}/desarquivar"><button class="sec" type="submit">Desarquivar</button></form>
<details class="excluir"><summary>Excluir a campanha</summary><form method="post" action="/painel/campanhas/{{.C.ID}}/excluir">
<label><input type="checkbox" name="confirmo" value="1" required> Entendo que a campanha some de vez; os jobs dela continuam em Automação › Jobs, sem a ligação com ela.</label>
<button class="perigo" type="submit">Excluir de vez</button></form></details>{{end}}
</div>{{end}}</div>
{{if .Resend}}<div class="card"><div class="card-cab"><h2>Reenviar o que não deu certo</h2><span class="muted">grupo de implantação atual</span></div>
<form class="limpeza" method="post" action="/painel/campanhas/{{.C.ID}}/reenviar">
<fieldset class="escolhas"><legend>O que reenviar</legend>
{{range .Resend}}<label class="opcao"><input type="checkbox" name="motivo" value="{{.Value}}" checked><span><b>{{.N}} job(s) {{.Label}}</b></span></label>{{end}}
</fieldset>
<p class="muted nota-limpeza">No próximo minuto o servidor refaz o plano de cada máquina e manda o job de novo; a que já não precisa (já em dia, sem o programa) sai como dispensada. A campanha volta a andar sem aceitar as falhas: o que falhar de novo pausa de novo.</p>
<div class="acoes"><button type="submit">Reenviar</button></div>
</form></div>{{end}}
{{if .Wakes}}<div class="card" id="ligar"><div class="card-cab"><h2>Ligar pela rede</h2><span class="muted">{{len .Wakes}} máquina(s)</span></div>
<div class="tabela"><table><thead><tr><th>Máquina</th><th>Grupo</th><th>Pedido</th><th>Acordou</th><th>Desligar</th></tr></thead>
<tbody>{{range .Wakes}}<tr><td><a href="/painel/maquinas/{{.MachineID}}?aba=hardware#ligar">{{.Name}}</a></td><td class="num">{{.Ring}}</td><td>{{.Wake}}</td><td>{{.Woke}}</td><td class="detalhe">{{template "leia-mais" (leia .Shutdown)}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{range .Rings}}{{$rv := .}}<div class="card" id="grupo-{{.Idx}}"><div class="card-cab"><h2>Grupo de implantação {{.Ring}}{{if .Current}} (atual){{end}}</h2><span class="muted">{{.Total}} job(s){{if and .Total (not .Selected)}} · <a href="{{.Href}}">ver os jobs</a>{{end}}</span></div>
{{if .Status}}<p class="muted">{{.Status}}</p>{{end}}
{{if .Counts}}<ul class="chips camp-estados" aria-label="jobs por estado">{{range .Counts}}<li><a class="chip{{if .Active}} ativo{{end}}" href="{{.Href}}"{{if .Active}} aria-current="true"{{end}}><span class="job {{.State}}">{{.Label}}</span> <b>{{.N}}</b></a></li>{{end}}</ul>{{end}}
{{if .Selected}}{{if .Total}}<form class="busca" method="get" action="/painel/campanhas/{{.CampaignID}}#grupo-{{.Idx}}"><input type="hidden" name="grupo" value="{{.Idx}}">{{if .Filter}}<input type="hidden" name="estado" value="{{.Filter}}">{{end}}
{{if .Slots}}<select name="programa" aria-label="programa"><option value="">Todos os programas</option>{{range .Slots}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>{{end}}
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Máquina: nome ou começo do ID" aria-label="buscar máquina">
<button type="submit">Filtrar</button>{{if or .Q .Filter .Slot}} <a href="{{.Href}}">Limpar</a>{{end}}</form>{{end}}
{{if .Jobs}}<div class="tabela"><table><thead><tr><th>Job</th><th>Máquina</th><th>Nome</th>{{if .HasSlot}}<th>Programa</th>{{end}}<th>Estado</th><th>Detalhe</th></tr></thead>
<tbody>{{range .Jobs}}<tr><td class="num">{{.ID}}</td><td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td>{{if $rv.HasSlot}}<td>{{.Slot}}</td>{{end}}
<td><span class="job {{.State}}">{{.Label}}</span></td><td class="detalhe">{{template "leia-mais" (leia .Detail)}}</td></tr>{{end}}</tbody></table></div>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next}}<a href="{{.Next}}">próxima ›</a>{{end}}</nav>
{{else if .Total}}<p class="muted">Nenhum job com esse filtro.</p>{{end}}{{end}}</div>{{end}}
{{template "fim"}}{{end}}

{{define "campanha-nao-encontrada"}}{{template "topo" .}}
<h1>Campanha não encontrada</h1>
<p><a href="/painel/campanhas">Voltar às campanhas</a>.</p>
{{template "fim"}}{{end}}

{{define "aneis"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Grupos de implantação e janelas</span></h1>
{{template "automacao-abas" "aneis"}}
<p class="muted">Campanhas passam pelos grupos em ordem: 0 é o piloto e 1 é o padrão (máquina nova entra no 1). Abra um grupo para ver as máquinas dele, adicionar outras (por filtro ou colando uma lista de nomes, IPs ou tombos), ligar prédios e faixas de IP e definir a janela de manutenção.</p>
<p class="muted">{{.Total}} máquina(s) no parque.</p>
<ul class="kpis">{{range .Cards}}<li><a class="kpi" href="{{.Href}}"><span class="kpi-rot">Grupo {{.Label}}</span><span class="kpi-val">{{.Machines}}</span><span class="kpi-sub">máquina(s){{if .Pinned}} · {{.Pinned}} à mão{{end}}{{if .Conflicts}} · <span class="v-laranja">{{.Conflicts}} em conflito</span>{{end}}</span>
<span class="kpi-sub">{{if .HasWindow}}Janela {{.Window}} · próxima {{.Next}}{{else}}Sem janela{{end}}</span>
<span class="kpi-sub">{{if or .Buildings .Ranges}}Regras: {{if .Buildings}}{{.Buildings}} prédio(s){{end}}{{if and .Buildings .Ranges}}, {{end}}{{if .Ranges}}{{.Ranges}} faixa(s) de IP{{end}}{{else}}Sem regras{{end}}</span></a></li>{{end}}</ul>
{{if .Empty}}<p class="muted">Grupos vazios: {{range $i, $c := .Empty}}{{if $i}}, {{end}}<a href="{{$c.Href}}">{{$c.Ring}}</a>{{end}}.</p>{{end}}
{{template "fim"}}{{end}}

{{define "grupo-abas"}}<nav class="abas" aria-label="abas do grupo"><a href="{{index .TabHrefs "maquinas"}}"{{if eq .Tab "maquinas"}} class="ativa" aria-current="page"{{end}}>Máquinas ({{.Machines}})</a>{{if .Admin}}<a href="{{index .TabHrefs "adicionar"}}"{{if eq .Tab "adicionar"}} class="ativa" aria-current="page"{{end}}>Adicionar</a><a href="{{index .TabHrefs "colar"}}"{{if eq .Tab "colar"}} class="ativa" aria-current="page"{{end}}>Colar lista</a>{{end}}<a href="{{index .TabHrefs "regras"}}"{{if eq .Tab "regras"}} class="ativa" aria-current="page"{{end}}>Regras e janela</a></nav>{{end}}

{{define "grupo-filtro"}}<form class="busca" method="get" action="/painel/grupos-implantacao/{{.Ring}}">{{if ne .Tab "maquinas"}}<input type="hidden" name="aba" value="{{.Tab}}">{{end}}
<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Nome, IP, tombo ou faixa (10.1.2.0/24)" aria-label="buscar máquina">
{{if .Buildings}}<select name="predio" aria-label="prédio">{{range .Buildings}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>{{end}}
{{if eq .Tab "maquinas"}}<select name="como" aria-label="como entrou">{{range .Origins}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>{{else}}<select name="de" aria-label="grupo atual">{{range .Froms}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select>{{end}}
<button type="submit">Buscar</button>{{if .HasFilter}} <a href="{{.ClearHref}}">Limpar</a>{{end}}</form>{{end}}

{{define "grupo"}}{{template "topo" .}}
<p><a href="/painel/grupos-implantacao">← Grupos de implantação</a></p>
<h1>Grupo de implantação <span class="muted">› {{.Label}}</span></h1>
<p class="muted">{{.Machines}} máquina(s){{if .Pinned}} · {{.Pinned}} movida(s) à mão{{end}}{{if .Conflicts}} · <span class="v-laranja">{{.Conflicts}} com regras em conflito</span>{{end}} · {{if .HasWindow}}janela {{.Window}}, próxima {{.Next}}{{else}}sem janela{{end}}</p>
{{template "grupo-abas" .}}
{{if eq .Tab "maquinas"}}<div class="card"><div class="card-cab"><h2>Máquinas do grupo</h2><span class="muted">"Como entrou": à mão (fica mesmo com regra), pelo prédio, pela faixa de IP, ou sem regra</span></div>
{{template "grupo-filtro" .}}
{{if .Rows}}{{if .Admin}}<form method="post" action="/painel/grupos-implantacao/mover"><input type="hidden" name="volta" value="{{.Ring}}"><input type="hidden" name="aba" value="maquinas">{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}{{end}}
<div class="tabela"><table>
<thead><tr>{{if .Admin}}<th class="col-lote">Lote</th>{{end}}<th>Máquina</th><th>Local</th><th>IP</th><th>Sistema</th><th>Último contato</th><th>Como entrou</th></tr></thead>
<tbody>{{range .Rows}}<tr>{{if $.Admin}}<td class="col-lote"><input type="checkbox" name="sel" value="{{.ID}}" aria-label="marcar {{.Name}}"></td>{{end}}<td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td><td>{{.Local}}</td><td><code>{{.IP}}</code></td><td>{{.OS}}</td><td>{{.LastSeen}}</td>
<td><span class="estado {{.Origin.Class}}">{{.Origin.Label}}</span>{{if .Origin.Conflict}}<div class="muted nota v-laranja">{{.Origin.Conflict}}</div>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if .Admin}}<div class="lote"><label>Mover para <select name="destino">{{range .Targets}}<option value="{{.Value}}">{{.Label}}</option>{{end}}</select></label>
<button type="submit">Conferir marcadas</button><button class="sec" type="submit" name="filtro" value="1">Conferir todas as {{.Found}}{{if .HasFilter}} do filtro{{end}}</button>
<button class="sec" type="submit" formaction="/painel/grupos-implantacao/devolver">Devolver marcadas às regras</button>
<span class="muted">Nada muda antes da conferência. "Devolver às regras" tira o "à mão": o prédio ou a faixa valem de novo.</span></div></form>{{end}}
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next2}}<a href="{{.Next2}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhuma máquina{{if .HasFilter}} com esse filtro{{else}} neste grupo{{end}}.</p>{{end}}</div>
{{else if eq .Tab "adicionar"}}<div class="card"><div class="card-cab"><h2>Adicionar ao grupo {{.Label}}</h2><span class="muted">máquinas de outros grupos</span></div>
{{template "grupo-filtro" .}}
{{if .Rows}}<form method="post" action="/painel/grupos-implantacao/mover"><input type="hidden" name="volta" value="{{.Ring}}"><input type="hidden" name="aba" value="adicionar"><input type="hidden" name="destino" value="{{.Ring}}">{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}
<div class="tabela"><table>
<thead><tr><th class="col-lote">Lote</th><th>Máquina</th><th>Grupo atual</th><th>Local</th><th>IP</th><th>Sistema</th><th>Último contato</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="col-lote"><input type="checkbox" name="sel" value="{{.ID}}" aria-label="marcar {{.Name}}"></td><td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td><td>{{.Group}}</td><td>{{.Local}}</td><td><code>{{.IP}}</code></td><td>{{.OS}}</td><td>{{.LastSeen}}</td></tr>{{end}}</tbody></table></div>
<div class="lote"><button type="submit">Conferir marcadas</button><button class="sec" type="submit" name="filtro" value="1">Conferir todas as {{.Found}}{{if .HasFilter}} do filtro{{end}}</button>
<span class="muted">A conferência mostra de que grupos elas saem. Até {{.MaxBatch}} por lote.</span></div></form>
<nav class="paginas" aria-label="páginas"><span class="muted">{{.Shown}}</span>{{if .Prev}}<a href="{{.Prev}}">‹ anterior</a>{{end}}{{if .Next2}}<a href="{{.Next2}}">próxima ›</a>{{end}}</nav>
{{else}}<p class="muted">Nenhuma máquina de outro grupo{{if .HasFilter}} com esse filtro{{end}}.</p>{{end}}</div>
{{else if eq .Tab "colar"}}<div class="card"><div class="card-cab"><h2>Colar lista</h2><span class="muted">um por linha: nome, IP ou tombo</span></div>
<p class="muted">Cole direto da planilha: vale a primeira coluna de cada linha, o nome com ou sem o domínio (<code>FJ088594</code>, <code>fj088594.tj.local</code>), o IP ou o tombo (<code>88594</code>). O servidor mostra o que achou, o que não achou (com nomes parecidos) e o que é ambíguo, antes de mover. Até {{.MaxPaste}} linhas.</p>
<form method="post" action="/painel/grupos-implantacao/colar"><input type="hidden" name="destino" value="{{.Ring}}">
<textarea name="lista" rows="12" required aria-label="lista de máquinas" placeholder="FJ088594&#10;FJ088595&#10;10.1.2.33&#10;88601"></textarea>
<div class="lote"><button type="submit">Procurar as máquinas</button><span class="muted">Nada muda antes da conferência.</span></div></form></div>
{{else}}{{template "grupo-regras" .}}{{end}}
{{template "fim"}}{{end}}

{{define "grupo-regras"}}<div class="card" id="nome"><div class="card-cab"><h2>Nome</h2><span class="muted">só para reconhecer o grupo; campanhas e jobs usam o número</span></div>
{{if .Admin}}<form class="grade" method="post" action="/painel/grupos-implantacao/nome"><input type="hidden" name="anel" value="{{.Ring}}">
<label for="nome-grupo">Nome do grupo {{.Ring}}</label><input id="nome-grupo" name="nome" value="{{.Name}}" maxlength="{{.MaxName}}" placeholder="ex.: Piloto TI, Fórum Cível">
<span></span><span><button type="submit">Gravar nome</button> <span class="muted">vazio apaga o nome</span></span></form>{{else}}<p>{{if .Name}}{{.Name}}{{else}}<span class="muted">sem nome</span>{{end}}</p>{{end}}</div>
<div class="card" id="predios"><div class="card-cab"><h2>Prédios</h2><span class="muted">do patrimônio (Configuração › Patrimônio)</span></div>
<p class="muted">As máquinas de um prédio ligado vão para este grupo: ao gravar, a cada importação do patrimônio e no primeiro inventário de uma máquina nova. O prédio vence a faixa de IP; quem foi movido à mão fica onde está.</p>
{{if .BuildingRows}}<div class="tabela"><table>
<thead><tr><th>Prédio</th><th>Máquinas no prédio</th><th>No grupo</th><th>Movidas à mão</th><th>Definido por</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .BuildingRows}}<tr><td>{{.Building}}{{if .Missing}} <span class="muted">fora do patrimônio atual</span>{{end}}</td><td class="num">{{.Machines}}</td><td class="num">{{.InRing}}</td><td class="num">{{.Pinned}}</td><td>{{.By}} · {{.Updated}}</td>
{{if $.Admin}}<td>{{if .Pinned}}<form class="inline" method="post" action="/painel/grupos-implantacao/predio/excecoes"><input type="hidden" name="predio" value="{{.Building}}"><button class="sec" type="submit">Devolver as {{.Pinned}} ao grupo</button></form> {{end}}<form class="inline" method="post" action="/painel/grupos-implantacao/predio/remover"><input type="hidden" name="predio" value="{{.Building}}"><button class="perigo" type="submit">Remover</button></form></td>{{end}}</tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum prédio ligado a este grupo.</p>{{end}}
{{if .Admin}}{{if .BuildingOpts}}<form class="grade" method="post" action="/painel/grupos-implantacao/predio"><input type="hidden" name="anel" value="{{.Ring}}">
<label for="predio">Ligar o prédio</label><select id="predio" name="predio" required>{{range .BuildingOpts}}<option value="{{.Name}}">{{.Name}} · {{.Machines}} máquina(s){{if ge .Ring 0}} · hoje ligado ao grupo {{.Ring}}{{end}}</option>{{end}}</select>
<span></span><label><input type="checkbox" name="excecoes" value="1"> Incluir as máquinas do prédio movidas à mão</label>
<span></span><span><button type="submit">Conferir</button> <span class="muted">a próxima tela diz quantas máquinas mudam</span></span>
</form>{{else if not .HasAssets}}<p class="muted">Importe o CSV do patrimônio em <a href="/painel/configuracao?aba=patrimonio">Configuração › Patrimônio</a> para ligar prédios.</p>{{end}}{{end}}</div>
<div class="card" id="faixas"><div class="card-cab"><h2>Faixas de IP</h2><span class="muted">pelos IPs das placas e do check-in</span></div>
<p class="muted">As máquinas com algum IP na faixa vão para este grupo, em até 5 minutos depois do check-in. Entre duas faixas, vale a menor; duas do mesmo tamanho que dizem grupos diferentes são conflito, e a máquina fica onde está.</p>
{{if .IPRows}}<div class="tabela"><table>
<thead><tr><th>Faixa</th><th>De – até</th><th>Máquinas na faixa</th><th>No grupo</th><th>Movidas à mão</th><th>Definida por</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .IPRows}}<tr><td><code>{{.Spec}}</code></td><td><code>{{.Range}}</code></td><td class="num">{{.Machines}}</td><td class="num">{{.InRing}}</td><td class="num">{{.Pinned}}</td><td>{{.By}} · {{.Updated}}</td>
{{if $.Admin}}<td><form class="inline" method="post" action="/painel/grupos-implantacao/faixa/remover"><input type="hidden" name="id" value="{{.ID}}"><button class="perigo" type="submit">Remover</button></form></td>{{end}}</tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma faixa ligada a este grupo.</p>{{end}}
{{if .Admin}}<form class="grade" method="post" action="/painel/grupos-implantacao/faixa"><input type="hidden" name="anel" value="{{.Ring}}">
<label for="faixa">Ligar a faixa</label><input id="faixa" name="faixa" required maxlength="64" placeholder="10.1.2.0/24, 10.1.2.10-50 ou 10.1.*">
<span></span><label><input type="checkbox" name="excecoes" value="1"> Incluir as máquinas da faixa movidas à mão</label>
<span></span><span><button type="submit">Conferir</button> <span class="muted">a próxima tela diz quantas máquinas mudam</span></span>
</form>{{end}}</div>
<div class="card" id="janela"><div class="card-cab"><h2>Janela de manutenção</h2><span class="muted">{{if .HasWindow}}{{.Window}} · próxima {{.Next}} · {{.WindowBy}}{{else}}sem janela{{end}}</span></div>
<p class="muted">Quando os jobs "na janela" deste grupo podem rodar. Uma janela que passa da meia-noite pertence ao dia em que começa (sex 22:00 por 6 h termina sábado às 04:00). Os jobs já criados mantêm a janela que levam assinada.</p>
{{if .Admin}}{{if .HasWindow}}<form class="inline" method="post" action="/painel/grupos-implantacao/{{.Ring}}/remover-janela"><button class="perigo" type="submit">Remover janela</button></form>{{end}}
<form class="grade" method="post" action="/painel/grupos-implantacao/janela"><input type="hidden" name="anel" value="{{.Ring}}">
<span>Dias</span><span class="dias">{{range .Days}}<label><input type="checkbox" name="dia" value="{{.Value}}"> {{.Label}}</label>{{end}}</span>
<label for="inicio">Início</label><input id="inicio" name="inicio" type="time" required value="12:00">
<label for="duracao">Duração</label><select id="duracao" name="duracao">{{range .Durations}}<option value="{{.Minutes}}" {{if eq .Minutes 120}}selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="fuso">Fuso</label><input id="fuso" name="fuso" type="text" required maxlength="64" value="{{.Timezone}}" placeholder="America/Porto_Velho">
<span></span><span><button type="submit">Gravar janela</button></span>
</form>{{end}}</div>{{end}}

{{define "grupo-mover"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Voltar</a></p>
<h1>Mover para o grupo {{.DestLabel}} <span class="muted">› conferência</span></h1>
<p class="aviso atencao">{{len .IDs}} máquina(s) vão para o grupo {{.DestLabel}}: {{range $i, $f := .From}}{{if $i}}, {{end}}{{$f.N}} saem do grupo {{$f.Label}}{{end}}.{{if .Already}} {{.Already}} já estavam nele.{{end}}{{if .Unknown}} {{.Unknown}} não estão mais no parque.{{end}}</p>
{{if .FilterTotal}}<p class="aviso atencao">O filtro tem {{.FilterTotal}} máquinas; este lote leva as {{.Max}} primeiras, por nome.</p>{{end}}
{{if .PinnedNote}}<p class="muted">Algumas têm regra (prédio ou faixa) que diz outro grupo. Movidas assim, passam a ser "à mão" e ficam neste grupo até alguém devolvê-las às regras.</p>{{else}}<p class="muted">Movidas assim, passam a ser "à mão": nenhuma regra de prédio ou faixa as tira deste grupo.</p>{{end}}
<form method="post" action="/painel/grupos-implantacao/mover"><input type="hidden" name="confirmar" value="1"><input type="hidden" name="destino" value="{{.Dest}}"><input type="hidden" name="volta" value="{{.BackRing}}"><input type="hidden" name="aba" value="{{.BackTab}}">{{range .IDs}}<input type="hidden" name="id" value="{{.}}">{{end}}
<div class="card"><div class="card-cab"><h2>Máquinas</h2><span class="muted">{{len .IDs}}</span></div>
<p>{{range $i, $n := .Names}}{{if $i}}, {{end}}{{$n}}{{end}}{{if .More}} e mais {{.More}}{{end}}.</p>
<div class="lote"><button type="submit">Mover {{len .IDs}} máquina(s)</button><a class="btn" href="{{.Back}}">Voltar sem mover</a></div></div></form>
{{template "fim"}}{{end}}

{{define "grupo-colar"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Colar lista</a></p>
<h1>Colar lista no grupo {{.DestLabel}} <span class="muted">› o que foi achado</span></h1>
<p class="aviso {{if .Found}}atencao{{else}}erro{{end}}">{{.Lines}} linha(s): {{len .Found}} máquina(s) para mover{{if .Already}}, {{len .Already}} já no grupo{{end}}{{if .Missing}}, {{len .Missing}} não achada(s){{end}}{{if .Ambiguous}}, {{len .Ambiguous}} ambígua(s){{end}}{{if .Repeated}}, {{.Repeated}} repetida(s){{end}}.</p>
{{if .Found}}<form method="post" action="/painel/grupos-implantacao/mover"><input type="hidden" name="destino" value="{{.Dest}}"><input type="hidden" name="volta" value="{{.Dest}}"><input type="hidden" name="aba" value="maquinas">{{range .IDs}}<input type="hidden" name="sel" value="{{.}}">{{end}}
<div class="card"><div class="card-cab"><h2>Achadas</h2><span class="muted">{{len .Found}}</span></div><div class="tabela"><table>
<thead><tr><th>Linha</th><th>Colado</th><th>Máquina</th><th>Achada pelo</th><th>Grupo atual</th></tr></thead>
<tbody>{{range .Found}}<tr><td class="num">{{.Line}}</td><td><code>{{.Token}}</code></td><td>{{.Name}}</td><td>{{.How}}</td><td>{{.Group}}</td></tr>{{end}}</tbody></table></div>
<div class="lote"><button type="submit">Conferir a mudança</button><a class="btn" href="{{.Back}}">Voltar</a></div></div></form>{{end}}
{{if .Missing}}<div class="card"><div class="card-cab"><h2>Não achadas</h2><span class="muted">{{len .Missing}}</span></div><div class="tabela"><table>
<thead><tr><th>Linha</th><th>Colado</th><th>Nomes parecidos</th></tr></thead>
<tbody>{{range .Missing}}<tr><td class="num">{{.Line}}</td><td><code>{{.Token}}</code></td><td>{{range $i, $n := .Similar}}{{if $i}}, {{end}}{{$n}}{{else}}<span class="muted">—</span>{{end}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{if .Ambiguous}}<div class="card"><div class="card-cab"><h2>Ambíguas</h2><span class="muted">acham mais de uma máquina; cole o nome completo</span></div><div class="tabela"><table>
<thead><tr><th>Linha</th><th>Colado</th><th>Máquinas</th></tr></thead>
<tbody>{{range .Ambiguous}}<tr><td class="num">{{.Line}}</td><td><code>{{.Token}}</code></td><td>{{range $i, $n := .Similar}}{{if $i}}, {{end}}{{$n}}{{end}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{if .Already}}<div class="card"><div class="card-cab"><h2>Já no grupo</h2><span class="muted">{{len .Already}}</span></div><p>{{range $i, $r := .Already}}{{if $i}}, {{end}}{{$r.Name}}{{end}}.</p></div>{{end}}
{{template "fim"}}{{end}}

{{define "grupo-regra"}}{{template "topo" .}}
<p><a href="{{.Back}}">← Regras do grupo</a></p>
<h1>{{if eq .Kind "predio"}}Ligar o prédio{{else}}Ligar a faixa{{end}} ao grupo {{.Label}} <span class="muted">› conferência</span></h1>
<p class="aviso atencao">{{if eq .Kind "predio"}}O prédio <strong>{{.Rule}}</strong>{{else}}A faixa <code>{{.Rule}}</code> ({{.RangeText}}){{end}} pega {{.Matched}} máquina(s). {{if .Move}}{{.Move}} vão para o grupo {{.Label}}: {{range $i, $f := .From}}{{if $i}}, {{end}}{{$f.N}} do grupo {{$f.Label}}{{end}}.{{else}}Nenhuma muda de grupo agora.{{end}}</p>
<ul class="muted">{{if .Already}}<li>{{.Already}} já estão no grupo.</li>{{end}}{{if .PinnedStay}}<li>{{.PinnedStay}} movida(s) à mão ficam onde estão{{if not .Excecoes}} (marque "incluir as movidas à mão" para trazê-las){{end}}.</li>{{end}}{{if .OtherRule}}<li>{{.OtherRule}} ficam por outra regra mais forte (o prédio vence a faixa; a faixa menor vence a maior).</li>{{end}}{{if .Conflicts}}<li class="v-laranja">{{.Conflicts}} com regras em conflito: a página do grupo mostra quais.</li>{{end}}<li>Máquinas que chegarem depois {{if eq .Kind "predio"}}entram no primeiro inventário{{else}}entram em até 5 minutos{{end}}.</li></ul>
<form method="post" action="{{.Action}}"><input type="hidden" name="confirmar" value="1">{{range .Hidden}}<input type="hidden" name="{{.Value}}" value="{{.Label}}">{{end}}
<div class="lote"><button type="submit">Gravar a regra</button><a class="btn" href="{{.Back}}">Voltar sem gravar</a></div></form>
{{template "fim"}}{{end}}

{{define "segbar"}}<div class="seg seg-{{.Size}} w{{.W}}" role="group" aria-label="{{.Label}}">{{range .Parts}}{{if .Href}}<a class="seg-p {{.Class}} w{{.W}}" href="{{.Href}}">{{else}}<span class="seg-p {{.Class}} w{{.W}}" tabindex="0">{{end}}{{if .Inline}}<span class="seg-rot">{{.Pct}}%</span>{{end}}<span class="seg-dica dica-{{.Align}}" role="tooltip">{{.Tip}}</span>{{if .Href}}</a>{{else}}</span>{{end}}{{end}}</div>{{end}}

{{define "estados-legenda"}}<ul class="seg-legenda"><li><i class="st-ok"></i>Em dia</li><li><i class="st-falta"></i>Faltando</li><li><i class="st-reinicio"></i>Reinício pendente</li><li><i class="st-sem"></i>Sem contato</li><li><i class="st-desc"></i>Desconhecido</li></ul>{{end}}

{{define "perf-grafico"}}<figure class="grafico-cartao">
<figcaption><span class="g-titulo">{{.Title}}</span>{{if .Now}} <span class="g-agora">{{.Now}}</span>{{end}}
{{if .Legend}}<span class="g-legenda">{{range .Series}}<span class="g-chave {{.Class}}">{{.Name}}</span>{{end}}</span>{{end}}{{if .Sub}}<span class="g-sub">{{.Sub}}</span>{{end}}</figcaption>
{{if .Empty}}<p class="muted">Sem medidas neste período.</p>{{else}}
<svg class="grafico" viewBox="0 0 {{.W}} {{.H}}" role="img" aria-label="{{.Label}}">
{{range .Bands}}<rect class="g-alerta" x="{{.X}}" y="{{$.T}}" width="{{.W}}" height="{{$.PlotH}}"/>{{end}}
{{range .Grid}}<line class="g-grade" x1="{{$.L}}" x2="{{$.R}}" y1="{{.Y}}" y2="{{.Y}}"/><text class="g-eixo" x="{{$.AxisX}}" y="{{.Y}}" dy="3" text-anchor="end">{{.Label}}</text>{{end}}
{{range .XTicks}}<text class="g-eixo" x="{{.X}}" y="{{$.AxisY}}" text-anchor="{{.Anchor}}">{{.Label}}</text>{{end}}
{{if .LimitLabel}}<line class="g-limite" x1="{{.L}}" x2="{{.R}}" y1="{{.LimitY}}" y2="{{.LimitY}}"/>{{end}}
{{range .Series}}{{$c := .Class}}{{range .Areas}}<polygon class="g-area {{$c}}" points="{{.}}"/>{{end}}{{range .Lines}}<polyline class="g-linha {{$c}}" points="{{.}}"/>{{end}}
{{if .Lines}}<circle class="g-ponta {{.Class}}" cx="{{.EndX}}" cy="{{.EndY}}" r="4"/>{{if .EndLabel}}<text class="g-valor" x="{{.EndTextX}}" y="{{.EndY}}" dy="4">{{.End}}</text>{{end}}{{end}}{{end}}
{{range .Zones}}<g class="g-zona"><rect class="g-alvo" x="{{.X}}" y="0" width="{{.W}}" height="{{$.B}}"/>
<g class="g-dica"><line class="g-guia" x1="{{.LineX}}" x2="{{.LineX}}" y1="{{$.T}}" y2="{{$.B}}"/>{{range .Dots}}<circle class="g-ponta {{.Class}}" cx="{{.X}}" cy="{{.Y}}" r="4"/>{{end}}
<rect class="g-caixa" x="{{.BoxX}}" y="{{.BoxY}}" width="{{.BoxW}}" height="{{.BoxH}}" rx="6"/><text class="g-quando" x="{{.TextX}}" y="{{.WhenY}}">{{.When}}</text>
{{range .Rows}}<line class="g-chave-linha {{.Class}}" x1="{{.KeyX1}}" x2="{{.KeyX2}}" y1="{{.Y}}" y2="{{.Y}}" transform="translate(0 -4)"/><text class="g-dica-valor" x="{{.ValueX}}" y="{{.Y}}">{{.Value}}</text><text class="g-dica-nome" x="{{.NameX}}" y="{{.Y}}">{{.Name}}</text>{{end}}
</g></g>{{end}}
</svg>{{end}}
</figure>{{end}}

{{define "alertas"}}{{template "topo" .}}
<h1>Alertas de desempenho</h1>
<div class="card"><div class="card-cab"><h2>{{if .All}}Todos{{else}}Abertos{{end}}</h2><span class="muted">{{.Open}} abertos · {{.Unacked}} sem reconhecimento</span></div>
<nav class="abas" aria-label="filtro dos alertas"><a href="/painel/alertas"{{if and (not .All) (eq .Metric "")}} class="ativa" aria-current="page"{{end}}>Abertos</a><a href="/painel/alertas?estado=todos"{{if and .All (eq .Metric "")}} class="ativa" aria-current="page"{{end}}>Todos (90 dias)</a><a href="/painel/alertas?medida=cpu{{if .All}}&amp;estado=todos{{end}}"{{if eq .Metric "cpu"}} class="ativa" aria-current="page"{{end}}>CPU</a><a href="/painel/alertas?medida=mem{{if .All}}&amp;estado=todos{{end}}"{{if eq .Metric "mem"}} class="ativa" aria-current="page"{{end}}>Memória</a><a href="/painel/alertas?medida=disk{{if .All}}&amp;estado=todos{{end}}"{{if eq .Metric "disk"}} class="ativa" aria-current="page"{{end}}>Disco</a></nav>
{{if .Batch}}<form id="alertas-lote" class="lote" method="post" action="/painel/alertas/lote">{{if .Metric}}<input type="hidden" name="medida" value="{{.Metric}}">{{end}}
<strong>Em lote:</strong>
<label><input type="radio" name="quais" value="marcados" checked> os marcados na tabela</label>
<label><input type="radio" name="quais" value="todos"> todos os {{.InFilter}} abertos{{if .Metric}} desta medida{{end}}</label>
<input name="nota" maxlength="{{.MaxNote}}" placeholder="nota (opcional)" aria-label="nota do lote">
<button type="submit" name="acao" value="fechar">Fechar</button> <button class="sec" type="submit" name="acao" value="reconhecer">Reconhecer</button> <button class="sec" type="submit" name="acao" value="limpar" title="só os alertas de disco">Limpar temporários</button>
<span class="muted">Nada muda antes da conferência.</span></form>{{end}}
{{if .Rows}}{{template "alertas-tabela" .}}{{else}}<p class="muted">{{if .All}}Nenhum alerta nos últimos 90 dias.{{else}}Nenhum alerta aberto.{{end}}</p>{{end}}
<p class="muted">Abre com: {{.Policy}}. Fecha quando a medida fica dentro do limite pelo mesmo tempo. Reconhecer tira do contador do menu, sem fechar. Fechar à mão (em lote) zera a contagem da medida: se o problema continuar, um alerta novo abre depois dos minutos seguidos.</p>
</div>
{{template "fim"}}{{end}}

{{define "alertas-tabela"}}<div class="tabela"><table>
<thead><tr>{{if $.Batch}}<th></th>{{end}}<th>Máquina</th><th>Medida</th><th>Aberto em</th><th>Duração</th><th>Pico</th><th>Último</th><th>Estado</th>{{if $.Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr>{{if $.Batch}}<td>{{if .Open}}<input type="checkbox" name="alerta" value="{{.ID}}" form="alertas-lote" aria-label="marcar o alerta {{.ID}}">{{end}}</td>{{end}}<td><a href="/painel/maquinas/{{.MachineID}}?aba=desempenho">{{if .Name}}{{.Name}}{{else}}<code>{{.Short}}</code>{{end}}</a></td><td>{{.Label}}</td><td>{{.Opened}}</td><td>{{.Duration}}</td><td class="num">{{.Peak}}</td><td class="num">{{.Last}}</td>
<td><span class="nivel-{{.Level}}">{{.State}}</span>{{if .Closed}}<br><span class="muted">{{.Closed}}</span>{{end}}{{if .Ack}}<br><span class="muted">{{.Ack}}</span>{{end}}</td>
{{if $.Admin}}<td>{{if .CanClean}}<form class="inline" method="post" action="/painel/alertas/lote"><input type="hidden" name="acao" value="limpar"><input type="hidden" name="quais" value="marcados"><input type="hidden" name="alerta" value="{{.ID}}"><button class="sec" type="submit">Limpar temporários</button></form> {{end}}{{if .CanAck}}<form class="inline" method="post" action="/painel/alertas/{{.ID}}/reconhecer"><input name="nota" maxlength="{{$.MaxNote}}" placeholder="nota (opcional)" aria-label="nota do reconhecimento do alerta {{.ID}}"> <button class="sec" type="submit">Reconhecer</button></form>{{end}}</td>{{end}}</tr>{{end}}</tbody></table></div>{{end}}

{{define "alertas-lote"}}{{template "topo" .}}
<p><a href="/painel/alertas{{if .Metric}}?medida={{.Metric}}{{end}}">← Alertas</a></p>
<h1>Conferir: {{.ActionLabel}} {{.N}} alerta(s)</h1>
{{if .Changed}}<p class="aviso atencao">A lista mudou desde a conferência (um alerta fechou, abriu ou foi reconhecido): confira de novo.</p>{{end}}
<div class="card"><dl class="dados">
<dt>Quais</dt><dd>{{if eq .Which "todos"}}todos os abertos{{else}}os marcados{{end}}{{if .MetricL}} de {{.MetricL}}{{end}}{{if eq .Action "reconhecer"}}, ainda sem reconhecimento{{end}}: {{.N}}</dd>
<dt>Nota</dt><dd>{{if .Note}}{{.Note}}{{else}}—{{end}}</dd>
</dl>
{{if eq .Action "fechar"}}<p class="aviso atencao">Fechar à mão tira o alerta da lista de abertos e zera a contagem da medida. Se o problema continuar, um alerta novo abre depois dos minutos seguidos: fechar não esconde problema que continua.</p>
{{else}}<p class="muted">Reconhecer tira do contador do menu; o alerta continua aberto até a medida voltar ao normal.</p>{{end}}
{{template "alertas-tabela" .}}
{{if .More}}<p class="muted">e mais {{.More}} alerta(s).</p>{{end}}
<form class="lote" method="post" action="/painel/alertas/lote">
<input type="hidden" name="acao" value="{{.Action}}"><input type="hidden" name="quais" value="{{.Which}}">{{if .Metric}}<input type="hidden" name="medida" value="{{.Metric}}">{{end}}
<input type="hidden" name="nota" value="{{.Note}}"><input type="hidden" name="lista" value="{{.Digest}}">
{{range .IDs}}<input type="hidden" name="alerta" value="{{.}}">{{end}}
<button type="submit" name="confirmar" value="1">{{.ActionLabel}} {{.N}} alerta(s)</button> <a href="/painel/alertas{{if .Metric}}?medida={{.Metric}}{{end}}">Cancelar</a>
</form></div>
{{template "fim"}}{{end}}

{{define "perguntas"}}{{template "topo" .}}
<h1>Automação <span class="muted">› Perguntas ao parque</span></h1>
{{template "automacao-abas" "perguntas"}}
<p class="muted">Uma pergunta de um catálogo fechado (só leitura) a todas as máquinas, ou a um grupo de implantação; as respostas chegam agrupadas. {{if .ChannelOn}}<strong>{{.Connected}}</strong> máquina(s) no canal em tempo real agora: essas respondem em segundos; as outras, no próximo check-in (a pergunta vale 1 h).{{else}}O canal em tempo real está desligado (Configuração › Parâmetros › Agentes): as máquinas respondem no próximo check-in (a pergunta vale 1 h).{{end}}</p>
{{if .CanAsk}}<section class="card"><div class="card-cab"><h2>Nova pergunta</h2></div>
<form class="grade" method="post" action="/painel/perguntas">
<div class="campo"><label for="sensor">Pergunta</label><select id="sensor" name="sensor">{{range .Sensors}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></div>
<div class="campo"><label for="arg">Sobre o quê</label><input id="arg" name="arg" type="text" required maxlength="300" value="{{.Arg}}" placeholder="caminho, processo, serviço, chave ou programa">
<small class="muted">{{range .Sensors}}<b>{{.Label}}</b> {{.Hint}}. {{end}}</small></div>
<div class="campo"><label for="nome">Nome do valor <span class="muted">(só registro)</span></label><input id="nome" name="nome" type="text" maxlength="128" value="{{.Name}}" placeholder="vazio: o valor padrão"></div>
<div class="campo"><label for="alvo">Para</label><select id="alvo" name="alvo"><option value="todas">todas as máquinas</option>{{range .Rings}}{{if .Machines}}<option value="{{.Ring}}">{{.Label}} · {{.Machines}} máq.</option>{{end}}{{end}}</select></div>
<div class="acoes"><button type="submit">Perguntar</button> <span class="muted">Só leitura: nada muda nas máquinas. Valores de registro com nome de senha voltam ocultos.</span></div>
</form></section>{{else}}<p class="aviso atencao">O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): as perguntas vão assinadas, como os jobs.</p>{{end}}
<div class="card"><div class="card-cab"><h2>Perguntas recentes</h2></div>{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>ID</th><th>Sensor</th><th>Pergunta</th><th>Para</th><th>Respostas</th><th>Por</th><th>Quando</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="num"><a href="/painel/perguntas/{{.ID}}">{{.ID}}</a></td><td>{{.Sensor}}</td><td><a href="/painel/perguntas/{{.ID}}"><code>{{.Question}}</code></a></td><td>{{.Target}}</td><td class="num">{{.Answered}} de {{.Machines}}</td><td>{{.By}}</td><td>{{.When}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma pergunta ainda.</p>{{end}}</div>
{{template "fim"}}{{end}}

{{define "pergunta"}}{{template "topo" .}}
<p><a href="/painel/perguntas">← Perguntas ao parque</a></p>
<h1>{{.Title}}: {{.Sensor}} <code>{{.Question}}</code></h1>
<div class="card"><dl class="dados">
<dt>Para</dt><dd>{{.Q.Target}} · {{.Q.Machines}} máquina(s)</dd>
<dt>Respostas</dt><dd>{{.Answered}} de {{.Q.Machines}}{{if .Waiting}} · {{.Waiting}} aguardando{{if .Live}} <span class="muted">(a página se atualiza sozinha)</span>{{end}}{{end}}</dd>
<dt>Por</dt><dd>{{.Q.CreatedBy}} · {{.When}}</dd>
</dl><p><a href="/painel/perguntas/{{.Q.ID}}/csv">Baixar CSV</a> (uma linha por máquina)</p></div>
{{range .Groups}}<div class="card"><div class="card-cab"><h2><span class="nivel-{{.Level}}">{{.Value}}</span></h2><span class="muted"><span class="barra" title="{{.Pct}}%"><span class="w{{.Pct}}"></span></span> {{.Count}} máquina(s) · {{.Pct}}%</span></div>
<details><summary>Ver as máquinas</summary><div class="tabela"><table><thead><tr><th>Máquina</th><th>Detalhe</th></tr></thead>
<tbody>{{range .Machines}}<tr><td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td><td class="muted">{{.Extra}}</td></tr>{{end}}</tbody></table></div>{{if .More}}<p class="muted">e mais {{.More}} (no CSV).</p>{{end}}</details></div>{{else}}<p class="muted">Nenhuma máquina no alvo.</p>{{end}}
{{template "fim"}}{{end}}

{{define "alertas-limpeza"}}{{template "topo" .}}
<p><a href="/painel/alertas{{if .Metric}}?medida={{.Metric}}{{end}}">← Alertas</a></p>
<h1>Limpar temporários: {{len .Machines}} máquina(s)</h1>
{{if .Changed}}<p class="aviso atencao">A lista de alertas mudou desde a conferência (um alerta fechou ou abriu): confira de novo.</p>{{end}}
{{if .NoKey}}<p class="aviso atencao">O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): a limpeza vai como job assinado.</p>{{end}}
<form class="card limpeza" method="post" action="/painel/alertas/lote">
<input type="hidden" name="acao" value="limpar"><input type="hidden" name="quais" value="{{.Which}}">{{if .Metric}}<input type="hidden" name="medida" value="{{.Metric}}">{{end}}
<input type="hidden" name="lista" value="{{.Digest}}">{{range .IDs}}<input type="hidden" name="alerta" value="{{.}}">{{end}}
<fieldset class="escolhas"><legend>O que limpar</legend>
{{range .Items}}{{if not .Optional}}<label class="opcao"><input type="checkbox" name="item" value="{{.Value}}" {{if .Checked}}checked{{end}}><span><b>{{.Label}}</b><small>{{.Hint}}</small></span></label>{{end}}{{end}}
</fieldset>
<fieldset class="escolhas"><legend>Opcionais <small>mais demorados; desmarcados por padrão</small></legend>
{{range .Items}}{{if .Optional}}<label class="opcao"><input type="checkbox" name="item" value="{{.Value}}" {{if .Checked}}checked{{end}}><span><b>{{.Label}}</b><small>{{.Hint}}</small></span></label>{{end}}{{end}}
</fieldset>
<p class="muted nota-limpeza">Sai só arquivo sem mudança há mais de 2 dias, e só das pastas acima: nada de Downloads, Documentos ou Lixeira. Arquivo em uso fica. O agente não segue links: um atalho de pasta plantado num temporário não leva a limpeza para outro lugar.</p>
<div class="tabela"><table><thead><tr><th>Máquina</th><th>Volumes em alerta</th></tr></thead>
<tbody>{{range .Machines}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=desempenho">{{.Name}}</a></td><td>{{.Volumes}}</td></tr>{{end}}</tbody></table></div>
<div class="acoes"><button type="submit" name="confirmar" value="1" {{if .NoKey}}disabled{{end}}>Limpar em {{len .Machines}} máquina(s)</button> <a href="/painel/alertas{{if .Metric}}?medida={{.Metric}}{{end}}">Cancelar</a></div>
</form>
{{template "fim"}}{{end}}

{{define "terminal-historico"}}{{template "topo" .}}
<p><a href="/painel/configuracao?aba=auditoria">← Auditoria</a></p>
<h1>Terminal do administrador <span class="muted">› histórico</span></h1>
<p class="muted">Todos os comandos enviados pelo terminal, do mais novo para o mais antigo, com quem mandou, de onde, o motivo da sessão, o código de saída e a saída. Nada aqui se edita nem se apaga pelo painel; o registro sai sozinho depois de 365 dias.</p>
<form class="busca" method="get" action="/painel/terminal"><input type="text" name="maquina" value="{{.Machine}}" placeholder="ID da máquina" aria-label="ID da máquina"> <input type="text" name="usuario" value="{{.User}}" placeholder="administrador" aria-label="administrador"> <button type="submit">Filtrar</button>{{if or .Machine .User}} <a href="/painel/terminal">limpar</a>{{end}}</form>
{{if .Rows}}<div class="terminal" role="log">{{range .Rows}}<div class="term-item"><div class="term-cab"><span class="term-prompt">{{.Prompt}}</span> <span class="term-cmd">{{.Command}}</span></div>
<div class="term-meta">#{{.ID}} · <a href="/painel/maquinas/{{.MachineID}}?aba=terminal">{{.Machine}}</a> · {{.User}}{{if .Remote}} ({{.Remote}}){{end}} · {{.When}} · <span class="job {{.Class}}">{{.State}}</span>{{if .Note}} · {{.Note}}{{end}} · motivo: {{.Reason}}</div>
{{if .HasOutput}}<details class="term-det"><summary>saída{{if .Truncated}} (cortada: ficou o fim){{end}}</summary><pre class="term-saida">{{.Output}}</pre></details>{{end}}</div>{{end}}</div>
<p class="acoes">{{if .Prev}}<a href="{{.Prev}}">‹ mais novos</a>{{end}} {{if .Next}}<a href="{{.Next}}">mais antigos ›</a>{{end}}</p>
{{else}}<p class="muted">Nenhum comando{{if or .Machine .User}} neste filtro{{end}}.</p>{{end}}
{{template "fim"}}{{end}}

{{define "bios-padrao"}}{{template "topo" .}}
<p><a href="/painel/configuracao?aba=bios">← Configuração › BIOS</a></p>
<h1>Padrão da BIOS</h1>
<p class="muted">O que toda máquina deve ter, de qualquer marca e modelo. O patchd lê as configurações que cada BIOS informa no inventário e resolve, máquina por máquina, o que mudar: só propõe o que a máquina mostrou que tem, com um valor que ela mostrou que aceita. O modelo que não tem a configuração fica marcado, sem tentativa às cegas.</p>
{{if .NoJobs}}<p class="aviso atencao">O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): a alteração vai como job assinado.</p>{{end}}
<div class="card">
<div class="card-cab"><h2>Conformidade</h2><span class="muted">{{.Total}} máquina(s)</span></div>
<div class="tabela"><table><thead><tr><th>No lote</th><th>Objetivo</th>{{range .Buckets}}<th class="num">{{.}}</th>{{end}}</tr></thead><tbody>
{{range .Goals}}<tr><td>{{if .Link}}<span class="muted">—</span>{{else}}<input form="padrao-filtro" type="checkbox" name="objetivo" value="{{.Key}}" id="obj-{{.Key}}"{{if .Checked}} checked{{end}} aria-label="incluir no lote: {{.Name}}">{{end}}</td>
<td>{{if .Link}}{{.Name}}<br><a href="{{.Link}}">senha em lote →</a>{{else}}<label for="obj-{{.Key}}">{{.Name}}</label>{{end}}</td>
{{range .Cells}}<td class="num">{{if and .N .Href}}<a href="{{.Href}}">{{.N}}</a>{{else}}{{.N}}{{end}}</td>{{end}}</tr>{{end}}
</tbody></table></div>
<p class="muted">Quem tem senha na BIOS fora da guarda aparece em "não dá agora": a alteração precisa da senha em uso, então essas máquinas passam antes pela senha em lote. Na linha da senha, "aguardando" são as gravações a caminho.</p>
<h3>Montar um lote</h3>
<form id="padrao-filtro" class="grade" method="get" action="/painel/bios/padrao">
<label for="padrao-grupo">Grupo de implantação</label><select id="padrao-grupo" name="grupo">{{range .Rings}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="padrao-limite">Tamanho do lote</label><select id="padrao-limite" name="limite">{{range .Sizes}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>
<span></span><span><button type="submit">Ver as máquinas do lote</button> <span class="muted">Nada é enviado neste passo. Comece por 1 máquina de cada modelo e confira na frente dela.</span></span>
</form>
</div>
{{if .Chosen}}<div class="card" id="conferencia"><div class="card-cab"><h2>Conferência</h2><span class="muted">{{.Selected}} máquina(s) neste lote</span></div>
{{if .Preview}}<h3>O que o padrão vira em cada fabricante</h3>
<div class="tabela"><table><thead><tr><th>Fabricante</th><th>Configuração</th><th>De</th><th>Para</th><th class="num">Máquinas</th></tr></thead><tbody>
{{range .Mapping}}<tr><td>{{.Vendor}}</td><td><b>{{.Title}}</b>{{if ne .Title .Name}}<br><small class="muted">na BIOS: {{.Name}}</small>{{end}}</td><td>{{.FromText}}{{if ne .FromText .From}}<br><small class="muted">{{.From}}</small>{{end}}</td><td><b>{{.ToText}}</b>{{if ne .ToText .To}}<br><small class="muted">{{.To}}</small>{{end}}</td><td class="num">{{.N}}</td></tr>{{end}}
</tbody></table></div>
<h3>As máquinas</h3>
<div class="tabela"><table><thead><tr><th>Máquina</th><th>Fabricante</th><th>Grupo</th><th>Alterações</th></tr></thead><tbody>
{{range .Preview}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=bios">{{.Name}}</a></td><td>{{.Vendor}}</td><td>{{.Ring}}</td><td>{{range $i, $c := .Changes}}{{if $i}}<br>{{end}}{{$c}}{{end}}</td></tr>{{end}}
</tbody></table></div>
{{if .More}}<p class="muted">e mais {{.More}} máquina(s), na ordem do nome.</p>{{end}}
<h3>Confirmar o lote</h3>
<form class="grade" method="post" action="/painel/bios/padrao" autocomplete="off">
{{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}<input type="hidden" name="lista" value="{{.Digest}}">
<label for="padrao-motivo">Motivo</label><input id="padrao-motivo" name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="chamado, ordem de serviço...">
<span></span><span><button type="submit">Aplicar o padrão em {{.Selected}} máquina(s)</button> <span class="muted">Cada máquina grava no próximo check-in; em vários modelos a mudança vale depois do reinício. Fica na auditoria.</span></span>
</form>
{{else}}<p class="muted">Nenhuma máquina a corrigir nos objetivos e no grupo escolhidos.</p>{{end}}
</div>{{end}}
{{if .Listing}}<div class="card" id="lista"><div class="card-cab"><h2>{{.Listing}}</h2><span class="muted"><a href="/painel/bios/padrao">fechar a lista</a></span></div>
{{if .ListRows}}<div class="tabela"><table><thead><tr><th>Máquina</th><th>Fabricante</th><th>Grupo</th><th>Na BIOS</th><th>Detalhe</th></tr></thead><tbody>
{{range .ListRows}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=bios">{{.Name}}</a></td><td>{{.Vendor}}</td><td>{{.Ring}}</td><td>{{range $i, $c := .Changes}}{{if $i}}<br>{{end}}{{$c}}{{else}}—{{end}}</td><td>{{if .Detail}}{{.Detail}}{{else}}—{{end}}</td></tr>{{end}}
</tbody></table></div>{{if .ListMore}}<p class="muted">e mais {{.ListMore}} máquina(s).</p>{{end}}
{{else}}<p class="muted">Nenhuma máquina nesta situação.</p>{{end}}
</div>{{end}}
{{template "fim"}}{{end}}

{{define "bios-lote"}}{{template "topo" .}}
<p><a href="/painel/configuracao?aba=bios">← Configuração › BIOS</a></p>
<h1>Senha da BIOS <span class="muted">› em lote</span></h1>
<p class="muted">O mesmo que o cartão Senha da BIOS faz numa máquina, para várias: o servidor sorteia uma senha por máquina, guarda-a cifrada e manda o job de gravação. Cada máquina grava no próximo check-in, com uma chamada só ao fabricante; se a BIOS recusar, o agente não repete.</p>
{{if not .On}}<p class="aviso atencao">Guarda desligada: o servidor está sem a chave de guarda (<code>PATCHD_BIOS_ESCROW_KEY_FILE</code>; crie com <code>patchd-server bios keygen</code>).</p>{{else}}
{{if .NoJobs}}<p class="aviso atencao">O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): a gravação vai como job assinado.</p>{{end}}
<div class="card">
<div class="card-cab"><h2>A frota</h2><span class="muted">{{.Total}} máquina(s) · chave de guarda <code>{{.KeyID}}</code></span></div>
<div class="tabela"><table><thead><tr><th>No lote</th><th>Situação</th><th class="num">Máquinas</th><th>O que acontece</th></tr></thead><tbody>
{{range .Counts}}<tr><td>{{if .Selectable}}<input form="lote-filtro" type="checkbox" name="sit" value="{{.Key}}" id="sit-{{.Key}}"{{if .Checked}} checked{{end}}{{if not .N}} disabled{{end}} aria-label="incluir no lote: {{.Name}}">{{else}}<span class="muted">—</span>{{end}}</td>
<td>{{if .Selectable}}<label for="sit-{{.Key}}">{{.Name}}</label>{{else}}{{.Name}}{{end}}</td><td class="num">{{if .N}}<a href="{{.Href}}">{{.N}}</a>{{else}}0{{end}}</td><td class="muted">{{.Does}}</td></tr>{{end}}
</tbody></table></div>
<h3>Montar um lote</h3>
<form id="lote-filtro" class="grade" method="get" action="/painel/bios/lote">
<label for="lote-grupo">Grupo de implantação</label><select id="lote-grupo" name="grupo">{{range .Rings}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>
<label for="lote-limite">Tamanho do lote</label><select id="lote-limite" name="limite">{{range .Sizes}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>
<span>Falhas</span><fieldset class="escolhas"><label class="opcao"><input type="checkbox" name="falhas" value="1"{{if .Failed}} checked{{end}}><span><b>Incluir as máquinas em que a última tentativa falhou</b><small>{{.FailedCount}} máquina(s) agora. As sem senha tentam de novo; as com senha, só com outra senha digitada na conferência. Com {{.MaxFailures}} falhas em 24 horas a máquina sai dos lotes: senha errada repetida trava a BIOS até reiniciar.</small></span></label></fieldset>
<span></span><span><button type="submit">Ver as máquinas do lote</button> <span class="muted">Nada é enviado neste passo. Comece por 1 máquina e confira na frente dela antes de seguir.</span></span>
</form>
</div>
{{if .Chosen}}<div class="card" id="conferencia"><div class="card-cab"><h2>Conferência</h2><span class="muted">{{.Selected}} máquina(s) neste lote</span></div>
{{if .Preview}}<div class="tabela"><table><thead><tr><th>Máquina</th><th>Fabricante</th><th>Grupo</th><th>Situação</th><th>Senha atual usada</th></tr></thead><tbody>
{{range .Preview}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=hardware">{{.Name}}</a>{{if .Detail}}<br><span class="muted">{{.Detail}}</span>{{end}}</td><td>{{.Vendor}}</td><td>{{.Ring}}</td><td>{{.Situation}}</td><td{{if .Level}} class="nivel-{{.Level}}"{{end}}>{{.Current}}</td></tr>{{end}}
</tbody></table></div>
{{if .More}}<p class="muted">e mais {{.More}} máquina(s), na ordem do nome.</p>{{end}}
<h3>Confirmar o lote</h3>
<form class="grade" method="post" action="/painel/bios/lote" autocomplete="off">
{{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}<input type="hidden" name="lista" value="{{.Digest}}">
{{if .AskCurrent}}<label for="lote-senha">Senha atual das máquinas com senha</label><span><input id="lote-senha" type="password" name="senha_atual" maxlength="{{.MaxCurrent}}" autocomplete="off"{{if .NeedTyped}} required{{end}} placeholder="{{if .NeedTyped}}a senha em uso nessas máquinas{{else}}em branco: a padrão cadastrada{{end}}">
<small class="muted">{{if .NeedTyped}}{{.NeedTyped}} máquina(s) deste lote só entram com a senha digitada (fabricante sem senha padrão cadastrada, ou a última tentativa falhou). {{end}}Digitada, ela vale para todas as máquinas "com senha, sem marcador" deste lote, no lugar da padrão cadastrada{{if .Defaults}} ({{.Defaults}}){{end}}. Vai cifrada para cada máquina e não fica guardada. As do marcador do <code>.bat</code> usam sempre a do Registro.</small></span>{{end}}
<label for="lote-motivo">Motivo</label><input id="lote-motivo" name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="chamado, ordem de serviço...">
<span></span><span><button type="submit">Gravar a senha em {{.Selected}} máquina(s)</button> <span class="muted">Fica na auditoria com quem pediu, o motivo e cada máquina.</span></span>
</form>
{{else}}<p class="muted">Nenhuma máquina nas situações e no grupo escolhidos.</p>{{end}}
</div>{{end}}
{{if .Listing}}<div class="card" id="lista"><div class="card-cab"><h2>{{.Listing}}</h2><span class="muted"><a href="/painel/bios/lote">fechar a lista</a></span></div>
{{if .ListRows}}<div class="tabela"><table><thead><tr><th>Máquina</th><th>Fabricante</th><th>Grupo</th><th>Detalhe</th></tr></thead><tbody>
{{range .ListRows}}<tr><td><a href="/painel/maquinas/{{.ID}}?aba=hardware">{{.Name}}</a></td><td>{{.Vendor}}</td><td>{{.Ring}}</td><td>{{if .Detail}}{{.Detail}}{{else}}—{{end}}</td></tr>{{end}}
</tbody></table></div>{{if .ListMore}}<p class="muted">e mais {{.ListMore}} máquina(s).</p>{{end}}
{{else}}<p class="muted">Nenhuma máquina nesta situação.</p>{{end}}
</div>{{end}}
{{end}}
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

{{define "bios-revelada"}}{{template "topo" .}}
<p><a href="/painel/maquinas/{{if .BackID}}{{.BackID}}/historico{{else}}{{.MachineID}}?aba=hardware#bios{{end}}">← {{.Name}}</a></p>
<h1>Senha da BIOS: {{.Name}}</h1>
<div class="card"><dl class="dados">
<dt>Senha</dt><dd><code class="senha-bios">{{.Grouped}}</code> <span class="muted">(digite sem os espaços; só letras minúsculas e números)</span></dd>
<dt>Guarda</dt><dd>#{{.Guard}} · {{.State}}</dd>
</dl>
<p class="aviso atencao">Esta revelação foi registrada na auditoria{{if .Mailed}} e avisada por e-mail à lista de notificações{{end}}. A página não fica guardada: não copie a senha para chamado, e-mail ou planilha.</p>
</div>
{{template "fim"}}{{end}}

{{define "nao-encontrada"}}{{template "topo" .}}
<h1>Máquina não encontrada</h1>
<p class="card">Ela não existe, está aposentada, ou o estado de compliance está indisponível. <a href="/painel/parque">Voltar ao parque</a>.</p>
{{template "fim"}}{{end}}

{{define "proibido"}}{{template "topo" .}}
<h1>Sem permissão</h1>
<p class="card">Esta ação é do perfil admin. <a href="/painel/parque">Voltar ao parque</a>.</p>
{{template "fim"}}{{end}}

{{define "scripts"}}{{template "topo" .}}
<h1>Automação</h1>
{{template "automacao-abas" "scripts"}}
{{if .NoStore}}<p class="aviso atencao">Sem PATCHD_CONTENT_DIR: o servidor não tem onde guardar os bundles.</p>{{end}}
{{if .NoJobKey}}<p class="aviso atencao">Sem chave de jobs: dá para subir bundles, mas não para executar.</p>{{end}}

<div class="card"><div class="card-cab"><h2>Novo pacote de script</h2><span class="muted">ZIP com arquivos auxiliares ou script avulso</span></div>
<p class="muted">Envie um ZIP de até {{.MaxMiB}} MiB ou um script de até 16 MiB. Windows: PowerShell, Batch e VBScript; Linux/macOS: Bash. O Run começa na raiz do pacote e pode acessar os arquivos por caminho relativo.</p>
<form class="formulario" method="post" action="/painel/scripts" enctype="multipart/form-data">
<div class="campo"><label for="script-nome">Nome do pacote</label><input id="script-nome" type="text" name="nome" maxlength="64" placeholder="opcional; usa o nome do arquivo"></div>
<div class="campo"><label for="script-nota">Descrição</label><input id="script-nota" type="text" name="nota" maxlength="200"></div>
<div class="campo"><label for="script-arquivo">Arquivo</label><input id="script-arquivo" type="file" name="arquivo" accept=".zip,.ps1,.bat,.cmd,.vbs,.sh" required></div>
<div class="campo"><label for="script-run">Run</label><input id="script-run" type="text" name="entrada" placeholder="instalar.cmd ou scripts/instalar.ps1">
<span class="muted">Caminho dentro do ZIP. Em script avulso ou pacote com uma única entrada, é preenchido automaticamente. Se houver várias entradas, escolha aqui ou antes de executar.</span></div>
<div class="campo"><label for="script-interpretador">Interpretador</label><select id="script-interpretador" name="interpretador"><option value="auto">Automático pela extensão do Run</option><option value="powershell">PowerShell (.ps1)</option><option value="cmd">Batch (.bat, .cmd)</option><option value="cscript">VBScript (.vbs)</option><option value="bash">Bash (.sh)</option><option value="exe">Executável Windows (.exe dentro do ZIP)</option></select></div>
<div class="campo"><label for="script-argumentos">Parâmetros padrão</label><input id="script-argumentos" type="text" name="argumentos" maxlength="65536" placeholder='/quiet MODO=1 ou -Destino "C:\Program Files\Empresa"'>
<span class="muted">Opcionais. Use aspas para agrupar valores com espaços. Caminhos são aceitos; Batch recusa operadores e expansão de variáveis.</span></div>
<div class="campo"><label for="script-minutos">Timeout (minutos)</label><input id="script-minutos" type="number" name="minutos" value="10" min="0.5" max="60" step="0.5" required></div>
<button type="submit"{{if .NoStore}} disabled{{end}}>Salvar pacote</button></form></div>

<div class="card"><div class="card-cab"><h2>Bundles</h2><span class="muted">{{len .Bundles}}</span></div>
{{if .Bundles}}<div class="tabela"><table>
<thead><tr><th>Nome</th><th>Sistema</th><th>Arquivos</th><th>Tamanho</th><th>Run padrão</th><th>SHA-256</th><th>Subido</th><th>Por</th><th></th></tr></thead>
<tbody>{{range .Bundles}}<tr><td>{{.Name}}{{if .Note}}<div class="muted nota">{{.Note}}</div>{{end}}</td>
<td>{{.OSLabel}}</td><td>{{.Files}}</td><td>{{.Size}}</td><td>{{if .Entry}}{{.Entry}}{{else}}Escolher ao executar{{end}}</td><td><code>{{.SHA}}…</code></td><td>{{.Uploaded}}</td><td>{{.By}}</td>
<td class="acoes"><a class="btn sec" href="/painel/scripts?bundle={{.ID}}">Configurar / Executar</a>
<details class="excluir"><summary>Excluir</summary><form method="post" action="/painel/scripts/{{.ID}}/excluir"><p class="muted">Remove o cadastro. Jobs já emitidos continuam podendo baixar o pacote.</p><button class="sec perigo" type="submit">Excluir cadastro</button></form></details></td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum pacote ainda. Envie um ZIP ou script acima.</p>{{end}}</div>

{{with .Chosen}}{{$chosen := .}}<div class="card" id="executar"><div class="card-cab"><h2>Executar: {{.Name}}</h2><span class="muted">{{.OSLabel}} · {{.Files}} arquivo(s)</span></div>
<form class="formulario" method="post" action="/painel/scripts/{{.ID}}/executar">
<div class="campo"><label for="entrada">Run</label>
<select id="entrada" name="entrada" required><option value="">Escolha o arquivo de entrada</option>{{range .Entries}}<option value="{{.}}"{{if eq . $chosen.Entry}} selected{{end}}>{{.}}</option>{{end}}</select>
<span class="muted">o arquivo do ZIP por onde a execução começa; ele chama os outros</span></div>
<div class="campo"><label for="argumentos">Argumentos</label>
<input type="text" id="argumentos" name="argumentos" value="{{.Arguments}}" placeholder="/quiet MODO=1" maxlength="65536">
<span class="muted">opcionais; use aspas para valores com espaços. Barras invertidas são preservadas. Em Batch, operadores e expansão de variáveis são recusados</span></div>
<div class="campo"><label for="minutos">Timeout (minutos)</label>
<input type="number" id="minutos" name="minutos" value="{{.TimeoutMinutes}}" min="0.5" max="60" step="0.5" required>
<span class="muted">ao vencer, a execução e seus processos filhos são interrompidos</span></div>
<p><button class="sec" type="submit" formaction="/painel/scripts/{{.ID}}/configurar">Salvar configuração padrão</button></p>
<p class="muted">Salvar não executa nada. Ao executar, os valores acima valem apenas para os jobs criados; salve primeiro se quiser reutilizá-los.</p>
<p class="muted">Marque as máquinas. O script roda com a conta do agente (SYSTEM no Windows, root no Linux), no próximo check-in de cada uma.</p>
{{if $.Machines}}<div class="tabela"><table>
<thead><tr><th></th><th>Máquina</th><th>Sistema</th><th>Grupo de implantação</th><th>Último contato</th></tr></thead>
<tbody>{{range $.Machines}}<tr><td><input type="checkbox" name="maquina" value="{{.ID}}" aria-label="{{.Name}}"></td>
<td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td><td>{{.OS}}</td><td>{{.Ring}}</td><td>{{.LastSeen}}</td></tr>{{end}}</tbody></table></div>
<p class="mt"><button type="submit"{{if $.NoJobKey}} disabled{{end}}>Executar nas máquinas marcadas</button></p>
{{else}}<p class="muted">Nenhuma máquina do sistema deste bundle no inventário.</p>{{end}}
</form></div>{{end}}
{{template "fim"}}{{end}}

{{define "mesclar-maquinas"}}{{template "topo" .}}
<p><a href="/painel/maquinas/{{.Target.ID}}">← Voltar ao equipamento</a></p><h1>Mesclar registros de equipamento</h1>
<div class="card"><div class="card-cab"><h2>Registro que continuará ativo</h2></div>
<dl class="dados"><dt>Nome</dt><dd>{{.Target.Name}}</dd><dt>ID</dt><dd><code>{{.Target.ID}}</code></dd><dt>Serial</dt><dd>{{.Target.Serial}}</dd><dt>Agente</dt><dd>{{.Target.Agent}}</dd><dt>Último contato</dt><dd>{{.Target.LastSeen}}</dd><dt>Senhas de BIOS guardadas</dt><dd>{{.Target.BIOS}}</dd></dl>
<p class="muted">Abra a tela a partir do registro da instalação atual, normalmente o de último contato mais recente. A credencial, as chaves e o estado atual desse agente serão mantidos.</p></div>
<div class="card"><div class="card-cab"><h2>Registro duplicado a incorporar</h2></div>
<form method="get" class="linha-faixa mescla-escolha"><select name="origem" required aria-label="Registro duplicado"><option value="">Selecione o registro antigo</option>{{range .Candidates}}<option value="{{.ID}}"{{if $.Source}}{{if eq $.Source.ID .ID}} selected{{end}}{{end}}>{{.Name}} · {{.ID}} · {{.Agent}} · {{.LastSeen}}</option>{{end}}</select><button class="sec" type="submit">Revisar mesclagem</button></form>
{{with .Source}}<dl class="dados"><dt>Nome</dt><dd>{{.Name}}</dd><dt>ID</dt><dd><code>{{.ID}}</code></dd><dt>Serial</dt><dd>{{.Serial}}</dd><dt>Agente</dt><dd>{{.Agent}}</dd><dt>Último contato</dt><dd>{{.LastSeen}}</dd><dt>Senhas de BIOS guardadas</dt><dd>{{.BIOS}}</dd></dl>
<p class="aviso atencao">Confirme que os dois registros representam o mesmo equipamento físico. Nome ou IP iguais, isoladamente, não comprovam isso. A credencial deste registro será revogada, e ele sairá do parque ativo.</p>
<p>Os relatórios, jobs, solicitações, auditoria e senhas de BIOS permanecerão vinculados aos IDs originais e poderão ser consultados no histórico do equipamento ativo. Jobs ainda pendentes na origem serão cancelados; jobs já entregues precisam terminar antes da mesclagem. A operação não instala nem altera o agente.</p>
<form method="post" action="/painel/maquinas/{{$.Target.ID}}/mesclar" class="descoberta-credencial">
<input type="hidden" name="origem" value="{{.ID}}"><div class="descoberta-campo"><label for="mescla-motivo">Motivo da mesclagem</label><input id="mescla-motivo" name="motivo" minlength="10" maxlength="500" required placeholder="Ex.: reinstalação do agente no mesmo equipamento"></div>
<label class="descoberta-opcao"><input type="checkbox" name="confirmo" value="sim" required><span>Conferi que é o mesmo equipamento e quero manter ativo o registro <code>{{$.Target.ID}}</code>.</span></label>
<button type="submit">Mesclar e preservar histórico</button></form>{{end}}</div>
{{template "fim"}}{{end}}

{{define "historico-maquina"}}{{template "topo" .}}
<p><a href="/painel/maquinas/{{.ID}}">← Voltar ao equipamento ativo</a></p><h1>Histórico e registros incorporados</h1>
<div class="card"><div class="card-cab"><h2>Identidades preservadas</h2><a class="btn sec" href="/painel/maquinas/{{.ID}}/mesclar">Mesclar outro registro</a></div>
<p class="muted">Nenhum histórico ou envelope cifrado foi transferido ou apagado. O registro ativo recebe novos dados; os registros incorporados conservam a origem dos dados antigos. A retenção normal dos relatórios continua valendo; as senhas de BIOS ficam fora dessa retenção.</p>
<div class="tabela"><table><thead><tr><th>Nome / ID</th><th>Estado</th><th>Mesclado em</th><th>Por</th><th>Motivo</th></tr></thead><tbody>{{range .Members}}<tr><td><a href="/painel/maquinas/{{$.ID}}/historico?registro={{.ID}}">{{.Name}}</a><br><code>{{.ID}}</code></td><td>{{if .Active}}ativo{{else}}incorporado · credencial revogada{{end}}</td><td>{{.When}}</td><td>{{.By}}</td><td>{{.Reason}}</td></tr>{{end}}</tbody></table></div></div>
<div class="card"><div class="card-cab"><h2>Relatórios do registro <code>{{.Selected}}</code></h2></div>
<p>Selecione uma identidade na tabela acima para consultar seus relatórios e jobs originais.</p>
<h3>Inventários</h3>{{if .Inventories}}<div class="tabela"><table><thead><tr><th>Recebido</th><th>Relatório</th></tr></thead><tbody>{{range .Inventories}}<tr><td>{{.At}}</td><td><a href="{{.Href}}">Ver inventário #{{.ID}} (JSON)</a></td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhum inventário retido para este registro.</p>{{end}}{{if .NextInv}}<p><a href="{{.NextInv}}">Inventários anteriores</a></p>{{end}}
<h3>Buscas de atualizações</h3>{{if .Scans}}<div class="tabela"><table><thead><tr><th>Recebida</th><th>Relatório</th></tr></thead><tbody>{{range .Scans}}<tr><td>{{.At}}</td><td><a href="{{.Href}}">Ver busca #{{.ID}} (JSON)</a></td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhuma busca retida para este registro.</p>{{end}}{{if .NextScan}}<p><a href="{{.NextScan}}">Buscas anteriores</a></p>{{end}}
<h3>Últimos 100 jobs</h3>{{if .Jobs}}<div class="tabela"><table><thead><tr><th>ID</th><th>Tipo</th><th>Estado</th><th>Resultado</th></tr></thead><tbody>{{range .Jobs}}<tr><td>{{.ID}}</td><td>{{.Type}}</td><td>{{.State}}</td><td>{{.Detail}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhum job retido.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Senhas de BIOS de todos os registros</h2></div>
<p class="muted">Cada estado é a última evidência registrada naquela identidade; uma senha antiga marcada como ativa não comprova que ainda seja a atual. As senhas continuam cifradas com a chave de guarda original. A revelação exige motivo e auditoria.</p>
{{if .BIOS}}<div class="tabela"><table><thead><tr><th>Guarda</th><th>Registro de origem</th><th>Estado registrado</th><th>Criada</th><th>Ação</th></tr></thead><tbody>{{range .BIOS}}<tr><td>{{.ID}}</td><td><code>{{.Origin}}</code></td><td>{{.State}}</td><td>{{.When}}</td><td>{{if .CanReveal}}<form method="post" action="/painel/maquinas/{{.Origin}}/bios/{{.ID}}/revelar" class="inline"><input name="motivo" minlength="10" maxlength="200" required placeholder="Motivo / chamado" aria-label="Motivo para revelar a guarda {{.ID}}"><button class="sec" type="submit">Revelar</button></form>{{else}}<span class="muted">Revelação indisponível: confira a chave de guarda e o estado.</span>{{end}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhuma senha de BIOS guardada nos registros.</p>{{end}}</div>
{{template "fim"}}{{end}}

{{define "descoberta"}}{{template "topo" .}}
<h1>Descoberta de rede</h1>
<div class="descoberta-acoes"><a class="btn sec" href="/painel/configuracao?aba=descoberta">Configurar instalação Windows</a><span class="muted">A sondagem parte do servidor patchd.</span></div>
{{with .Config}}{{if .Blocker}}<p class="aviso atencao" role="status">{{.Blocker}} <a href="/painel/configuracao?aba=descoberta">Ver configuração</a></p>{{end}}{{end}}
{{if .Installs}}<div class="card"><div class="card-cab"><h2>Instalações automáticas</h2></div><p>Aguardando registro significa que o serviço foi verificado na estação. O registro e o inventário ainda precisam ser recebidos para confirmar a presença do agente.</p><div class="tabela"><table><thead><tr><th>IP</th><th>Resultado</th><th>Atualizado</th><th>Solicitado por</th></tr></thead><tbody>{{range .Installs}}<tr><td>{{.IP}}</td><td>{{.State}}</td><td>{{.At}}</td><td>{{.By}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
<div class="card descoberta-status" aria-live="polite"><div class="card-cab"><h2>{{if .Scanning}}Varrendo… · {{.Phase}}{{else if .Phase}}{{.Phase}}{{else if .LastRun}}Última varredura concluída{{else}}Aguardando varredura{{end}}</h2>{{if .Scanning}}<span class="muted">{{.Elapsed}} · atualização a cada 3 segundos</span>{{end}}</div>
{{if .Scanning}}<label for="descoberta-progresso">{{.ScanDone}} de {{.ScanTotal}} endereços processados · {{.Found}} com portas abertas</label><progress id="descoberta-progresso" max="{{.ScanTotal}}" value="{{.ScanDone}}"></progress>{{if .LastIP}}<p class="muted">Último endereço sondado: <code>{{.LastIP}}</code></p>{{end}}{{end}}
{{if .Diagnostics}}<p class="muted">{{.Diagnostics}}</p>{{end}}
{{if .Detail}}<p>{{.Detail}}</p>{{else if .LastRun}}<p>A sondagem terminou. Consulte os totais e o diagnóstico em Últimas varreduras; não há varredura em andamento neste servidor.</p>{{else}}<p>Cadastre uma faixa e clique em Varrer agora. A tela mostrará a sondagem, a consulta ao inventário e cada tentativa de instalação.</p>{{end}}
{{if and .LastRun (eq .Alive 0)}}<p class="aviso atencao">A última varredura não recebeu respostas nas portas 445, 3389, 5986 e 22. Isso não comprova que os equipamentos estejam desligados: pode haver bloqueio no firewall, falta de rota ou serviços desabilitados. Verifique a conectividade a partir da VM ou contêiner do servidor patchd.</p>{{end}}
{{if .Events}}<details class="descoberta-ajuda" open><summary>Atividade desta execução</summary><ol class="descoberta-atividade">{{range .Events}}<li><time>{{.At}}</time><span>{{.Detail}}</span></li>{{end}}</ol><p class="muted">Últimos 30 eventos deste processo. Resultados por estação e histórico de varreduras permanecem no banco.</p></details>{{end}}</div>

<div class="card"><div class="card-cab"><h2>Faixas de busca</h2><span class="muted">{{len .Ranges}} de {{.MaxRanges}}</span></div>
<p class="muted">CIDR (<code>10.0.0.0/24</code>) ou intervalo (<code>10.0.0.10-60</code>). O servidor só alcança as sub-redes que a VM enxerga.</p>
<form method="post" action="/painel/descoberta/faixas" class="linha-faixa">
<input type="text" name="faixa" placeholder="10.0.0.0/24" required aria-label="faixa de IP">
<input type="text" name="nota" placeholder="descrição (opcional)" maxlength="120" aria-label="descrição">
<button type="submit">Cadastrar</button></form>
{{if .Ranges}}<div class="tabela"><table>
<thead><tr><th>Faixa</th><th>Descrição</th><th>Estado</th><th>Criada</th><th></th></tr></thead>
<tbody>{{range .Ranges}}<tr><td><code>{{.Spec}}</code></td><td>{{if .Note}}{{.Note}}{{else}}<span class="muted">—</span>{{end}}</td>
<td>{{if .Enabled}}<span class="descoberta-estado ok">ligada</span>{{else}}<span class="muted">desligada</span>{{end}}</td><td>{{.Created}}</td>
<td class="acoes">{{if .Enabled}}<form method="post" action="/painel/descoberta/faixas/{{.ID}}/desligar" class="inline"><button class="sec" type="submit">Desligar</button></form>{{else}}<form method="post" action="/painel/descoberta/faixas/{{.ID}}/ligar" class="inline"><button class="sec" type="submit">Ligar</button></form>{{end}}
<form method="post" action="/painel/descoberta/faixas/{{.ID}}/remover" class="inline"><button class="sec perigo" type="submit">Remover</button></form></td></tr>{{end}}</tbody></table></div>{{end}}
<form method="post" action="/painel/descoberta/varrer" class="mt"><button type="submit"{{if .Scanning}} disabled{{end}}>Varrer agora</button>{{if .LastRun}} <span class="muted">última: {{.LastRun}}</span>{{end}}</form></div>

<div class="card"><div class="card-cab"><h2>Sem agente</h2><span class="muted">{{len .Hosts}} host(s){{if .Alive}} · {{.WithAgent}} com agente de {{.Alive}} vivos{{end}}</span></div>
{{if not .PublicURL}}<p class="aviso atencao">Sem PATCHD_PUBLIC_URL: os comandos saem com &lt;endereço-do-servidor&gt; a preencher.</p>{{end}}
{{if .Hosts}}<p class="muted">A instalação automática usa a credencial de Configuração › Descoberta. Para instalação manual, crie um token em <a href="/painel/configuracao?aba=agente">Configuração › Agente</a> e salve o <code>patchd-token.txt</code> junto do instalador.</p>
<div class="tabela"><table><thead><tr><th>IP</th><th>Nome (DNS)</th><th>Portas</th><th>Palpite</th><th>Visto</th><th></th></tr></thead>
<tbody>{{range .Hosts}}<tr><td><code>{{.IP}}</code></td><td>{{if .Hostname}}{{.Hostname}}{{else}}<span class="muted">—</span>{{end}}</td>
<td>{{.Ports}}</td><td>{{.OSLabel}}</td><td>{{.LastSeen}}</td>
<td><details class="host"><summary>Instalar</summary>{{range .Hints}}<div class="metodo"><p class="rot-metodo">{{.Label}}</p><pre class="comando">{{.Command}}</pre><p class="muted nota">{{.Note}}</p></div>{{end}}</details></td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum host sem agente na última varredura.{{if not .LastRun}} Cadastre uma faixa e clique em Varrer agora.{{end}}</p>{{end}}</div>

{{if .Runs}}<div class="card"><div class="card-cab"><h2>Últimas varreduras</h2></div>
<div class="tabela"><table><thead><tr><th>Quando</th><th>Por</th><th>Endereços</th><th>Com portas abertas</th><th>Sem agente</th><th>Diagnóstico</th></tr></thead>
<tbody>{{range .Runs}}<tr><td>{{.When}}</td><td>{{.By}}</td><td>{{.Scanned}}{{if .Partial}} <span class="muted">(parcial)</span>{{end}}</td><td>{{.Alive}}</td><td>{{.SemAg}}</td><td>{{if .Note}}{{.Note}}{{else}}Concluída{{end}}</td></tr>{{end}}</tbody></table></div></div>{{end}}
{{template "fim"}}{{end}}
` + dashboardTemplates + standardizationTemplates))
