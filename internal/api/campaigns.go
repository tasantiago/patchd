package api

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/window"
	"github.com/tasantiago/patchd/internal/wuerr"
)

// As campanhas no painel. Aula 8.5: a lista, só leitura. Aula 11.2: o admin cria (com uma
// tela de conferência antes, porque a campanha vira jobs em máquinas de verdade no minuto
// seguinte), vê o andamento de cada grupo de implantação e pausa, retoma ou cancela, com as mesmas regras
// da linha de comando (campaign.Change).

const campaignsShown = 50

// campaignsScan: quantas campanhas a lista lê para separar as arquivadas.
const campaignsScan = 2000

// As opções do formulário: listas fechadas, como na janela dos grupos de implantação.
var (
	observeHours = []int{0, 1, 4, 12, 24, 48, 72, 168}
	ttlHours     = []int{1, 3, 6, 12, 24, 72}
	// sourcePattern: nome de pacote fonte do Debian/Ubuntu ou do Fedora.
	sourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,99}$`)
)

type campaignRow struct {
	ID                                                   int64
	Name, Type, Sources, Rings, State, Label, Detail, By string
	Created                                              string
	Legacy                                               bool // pelos grupos de implantação numéricos
}

// ringChoice é um grupo de implantação no formulário, com o que o admin precisa saber antes de marcar.
type ringChoice struct {
	Ring     int
	Label    string
	Machines int
	Window   string // "" = sem janela
	Checked  bool
}

type hourOption struct {
	Hours    int
	Label    string
	Selected bool
}

// programOption: uma versão aprovada e guardada de um programa de terceiros (Aula 14.8).
type programOption struct {
	Value, Label string // "libreoffice 26.8.1.1"; "LibreOffice 26.8.1.1"
	Selected     bool
}

// campaignForm é o formulário (também o que volta para corrigir e o que vai na conferência).
type campaignForm struct {
	Name, Type, Sources, When string
	Program                   string          // thirdparty: "programa versão"
	Programs                  []programOption // as versões que podem virar campanha
	TPOn                      bool            // o servidor tem as regras de terceiros
	// Scope: os prédios, os andares e o piloto (campaignscope.go). As campanhas novas não
	// escolhem grupos de implantação numéricos.
	Scope          scopeForm
	scopeIn        scopeInput
	Observe, TTL   []hourOption
	ObserveH, TTLH int
	Delay          int // reboot (Aula 16.5): aviso em minutos
	Delays         []int
	// Em horário fixo todo dia: a hora (HH:MM), a duração (minutos) e o fuso do painel.
	DailyStart string
	DailyDur   int
	Durations  []durationOption
	TZ         string
	// Close: terceiros com o programa aberto: 0 adia; N fecha com aviso de N minutos (só com horário).
	Close int
	// Wake (Aula 22.3): ligar pela rede na janela e desligar depois.
	Wake bool
	// Resend: reenvios automáticos das falhas (expirado e falhou), por máquina; 0..5.
	Resend  int
	Resends []int
	// Microsoft: a campanha do Windows usa a fonte reserva; MicrosoftOn: a reserva está ligada em Parâmetros.
	Microsoft   bool
	MicrosoftOn bool
	// A campanha de remoção (desinstalar): o programa de Inventário › Programas instalados (o
	// sistema, a chave do nome e o fabricante) e as versões marcadas.
	RemoveOS, RemoveKey, RemovePub string
	RemoveVersions                 []string
}

type campaignsView struct {
	pageBase
	Rows []campaignRow
	// Archived: a lista das arquivadas (?arquivadas=1); NArchived, quantas há; NFinished,
	// quantas encerradas ainda na lista (o botão de arquivá-las de uma vez).
	Archived             bool
	NArchived, NFinished int
	CanNew               bool // admin e chave de jobs
	NoKey                bool // admin sem a chave: o formulário não aparece
	Form                 campaignForm
}

func campaignLabel(s string) string {
	switch s {
	case campaign.StateRunning:
		return "em andamento"
	case campaign.StateDone:
		return "concluída"
	}
	return s
}

func hoursLabel(h int) string {
	switch {
	case h == 0:
		return "sem observação"
	case h%24 == 0 && h >= 24:
		if h == 24 {
			return "1 dia"
		}
		return fmt.Sprintf("%d dias", h/24)
	case h == 1:
		return "1 hora"
	}
	return fmt.Sprintf("%d horas", h)
}

func hourOptions(list []int, sel int) []hourOption {
	out := make([]hourOption, len(list))
	for i, h := range list {
		out[i] = hourOption{Hours: h, Label: hoursLabel(h), Selected: h == sel}
	}
	return out
}

// ringChoices monta os grupos de implantação 0..9 com quantas máquinas e a janela de cada um.
func (a *api) ringChoices(r *http.Request, checked []int) ([]ringChoice, error) {
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		return nil, err
	}
	windows, err := a.store.Windows(r.Context())
	if err != nil {
		return nil, err
	}
	count := map[int]int{}
	for _, m := range machines {
		count[m.Ring]++
	}
	out := make([]ringChoice, 0, store.MaxRing+1)
	for ring := 0; ring <= store.MaxRing; ring++ {
		c := ringChoice{Ring: ring, Label: ringLabel(ring), Machines: count[ring]}
		if rw, ok := store.WindowFor(windows, ring); ok {
			c.Window = rw.Window.String()
		}
		for _, x := range checked {
			if x == ring {
				c.Checked = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *api) campaignsPage(w http.ResponseWriter, r *http.Request) {
	v := campaignsView{pageBase: a.base(r, "Campanhas", "campanhas")}
	v.CanNew, v.NoKey = v.Admin && a.jobKey != nil, v.Admin && a.jobKey == nil
	switch q := r.URL.Query(); {
	case q.Has("arquivada"):
		v.Flash = fmt.Sprintf("Campanha %s arquivada: saiu da lista, sem perder nada. Ela fica em Arquivadas, de onde volta ou é excluída.", numOnly(q.Get("arquivada")))
	case q.Has("arquivadas-agora"):
		v.Flash = fmt.Sprintf("%s campanha(s) encerrada(s) arquivada(s).", numOnly(q.Get("arquivadas-agora")))
	case q.Has("excluida"):
		v.Flash = fmt.Sprintf("Campanha %s excluída. Os jobs dela continuam em Automação › Jobs.", numOnly(q.Get("excluida")))
	}
	if v.CanNew {
		f := campaignForm{Type: jobs.TypeRescan, When: "prazo", ObserveH: 24, TTLH: 24, Delay: 15, Delays: rebootDelays, Resends: resendChoices,
			DailyStart: "02:00", DailyDur: 180}
		// Vindo de "Campanha por prédio" na página do programa (Aula 14.8): já preenchido.
		if q := r.URL.Query(); q.Get("tipo") == jobs.TypeThirdParty {
			f.Type, f.Program = jobs.TypeThirdParty, q.Get("programa")
			f.Name = f.Program
		}
		// Vindo do cartão do Windows na máquina (Aula 16.4): o KB já preenchido.
		if q := r.URL.Query(); q.Get("tipo") == jobs.TypeWinUpdate {
			if kb, ok := jobs.NormalizeKB(q.Get("kb")); ok {
				f.Type, f.Sources, f.Name = jobs.TypeWinUpdate, kb, "Windows "+kb
			}
		}
		// Aula 16.6: ?tipo=reboot já abre com o reinício escolhido.
		if r.URL.Query().Get("tipo") == jobs.TypeReboot {
			f.Type, f.Name = jobs.TypeReboot, "Reinício pendente"
		}
		o, err := a.deployOverview(r.Context())
		if err != nil {
			a.internalError(w, r, "listar os prédios", err)
			return
		}
		q := r.URL.Query()
		f.Scope = scopeFormFrom(o, scopeInput{All: q.Get("todos") == "1"}, texto.Clip(q.Get("predios"), 100))
		if err := a.programChoices(r, &f); err != nil {
			a.internalError(w, r, "listar versões aprovadas", err)
			return
		}
		f.Observe, f.TTL = hourOptions(observeHours, f.ObserveH), hourOptions(ttlHours, f.TTLH)
		v.Form = f
	}
	a.renderCampaigns(w, r, http.StatusOK, v)
}

func (a *api) renderCampaigns(w http.ResponseWriter, r *http.Request, status int, v campaignsView) {
	v.Form.TZ = a.panelLocation().String()
	v.Form.MicrosoftOn = a.microsoftAllowed()
	v.Form.Durations = nil
	for _, m := range windowDurations {
		v.Form.Durations = append(v.Form.Durations, durationOption{Minutes: m, Label: durationLabel(m)})
	}
	list, err := a.store.Campaigns(r.Context(), campaignsScan)
	if err != nil {
		a.internalError(w, r, "listar campanhas", err)
		return
	}
	v.Archived = r.URL.Query().Get("arquivadas") == "1"
	shown := 0
	for _, c := range list {
		if c.ArchivedAt != nil {
			v.NArchived++
		} else if c.Finished() {
			v.NFinished++
		}
		if (c.ArchivedAt != nil) != v.Archived || shown >= campaignsShown {
			continue
		}
		shown++
		typ := typeLabel(c.Type)
		if c.UseWindow {
			typ += ", na janela"
		}
		if c.Daily != nil {
			typ += ", às " + fmt.Sprintf("%02d:%02d", int(c.Daily.Start.Hours()), int(c.Daily.Start.Minutes())%60)
		}
		v.Rows = append(v.Rows, campaignRow{ID: c.ID, Name: c.Name, Type: typ, Sources: dash(c.Target()),
			Rings: stagesProgress(c), Legacy: !c.Located(), State: c.State, Label: campaignLabel(c.State), Detail: dash(c.Detail),
			By: c.CreatedBy, Created: ago(a.now().Sub(c.CreatedAt))})
	}
	a.render(w, r, status, "campanhas", v)
}

// stagesProgress: as etapas, com a atual entre colchetes; na legada, os grupos ("[0] → 1").
func stagesProgress(c campaign.Campaign) string {
	if c.Located() {
		return c.StagesText(true)
	}
	return ringsProgress(c)
}

// ringsProgress: "[0] → 1", com o grupo de implantação atual entre colchetes.
func ringsProgress(c campaign.Campaign) string {
	rings := make([]string, len(c.Rings))
	for i, x := range c.Rings {
		rings[i] = strconv.Itoa(x)
		if i == c.CurrentIdx && c.State != campaign.StateDone {
			rings[i] = "[" + rings[i] + "]"
		}
	}
	return strings.Join(rings, " → ")
}

// parseCampaignForm lê e confere o formulário e resolve o escopo (o plano por prédio e andar,
// calculado no servidor a partir de o). A mensagem de erro é para a tela.
func (a *api) parseCampaignForm(r *http.Request, o *deployOverview) (campaign.Campaign, []campaign.Target, campaignForm, string) {
	c, f, problem := a.parseCampaignFields(r, o)
	if problem != "" {
		return c, nil, f, problem
	}
	_, sc, ts, problem := a.scopeFromRequest(r, o)
	if problem != "" {
		return c, nil, f, problem
	}
	c.Scope = &sc
	if err := c.Validate(); err != nil {
		return c, nil, f, "Campanha inválida: " + err.Error() + "."
	}
	if c.UseWindow {
		if problem := windowProblem(o, ts); problem != "" {
			return c, nil, f, problem
		}
	}
	return c, ts, f, ""
}

// parseCampaignFields lê os campos da campanha (sem o escopo).
func (a *api) parseCampaignFields(r *http.Request, o *deployOverview) (campaign.Campaign, campaignForm, string) {
	_ = r.ParseForm()
	f := campaignForm{Name: strings.TrimSpace(r.PostFormValue("nome")), Type: r.PostFormValue("tipo"),
		Sources: strings.TrimSpace(r.PostFormValue("fontes")), When: r.PostFormValue("quando"), Program: r.PostFormValue("programa")}
	f.ObserveH, _ = strconv.Atoi(r.PostFormValue("observacao"))
	f.TTLH, _ = strconv.Atoi(r.PostFormValue("prazo"))
	f.Delay, _ = strconv.Atoi(r.PostFormValue("aviso"))
	f.Delays, f.Resends = rebootDelays, resendChoices
	f.DailyStart = strings.TrimSpace(r.PostFormValue("hora"))
	f.DailyDur, _ = strconv.Atoi(r.PostFormValue("duracao"))
	f.Microsoft = r.PostFormValue("microsoft") == "1" && (f.Type == jobs.TypeWinUpdate || f.Type == campaign.TypeOSAll)
	if f.Type == jobs.TypeThirdParty || f.Type == campaign.TypeThirdPartyAll {
		f.Close, _ = strconv.Atoi(r.PostFormValue("fechar"))
	}
	f.MicrosoftOn = a.microsoftAllowed()
	f.Resend, _ = strconv.Atoi(r.PostFormValue("reenvios"))
	f.Wake = r.PostFormValue("ligar") == "1"
	f.scopeIn = readScopeInput(r.PostForm)
	f.Scope = scopeFormFrom(o, f.scopeIn, "")
	c := campaign.Campaign{Name: f.Name, Type: f.Type, Observe: time.Duration(f.ObserveH) * time.Hour,
		TTL: time.Duration(f.TTLH) * time.Hour, UseWindow: f.When == "janela", MicrosoftFallback: f.Microsoft, Wake: f.Wake,
		CloseDelay: time.Duration(f.Close) * time.Minute, AutoResend: f.Resend}
	if f.Type == jobs.TypeReboot {
		c.RebootDelay = time.Duration(f.Delay) * time.Minute
	}
	if f.Type == jobs.TypeUninstall {
		t, problem := a.removeTargetFromForm(r, &f)
		if problem != "" {
			return c, f, problem
		}
		c.Remove = t
	}
	if f.Type == jobs.TypeThirdParty {
		// O select manda "programa versão"; nos outros tipos ele vai junto e é ignorado.
		parts := strings.Fields(f.Program)
		if len(parts) != 2 {
			return c, f, "Escolha o programa e a versão aprovada."
		}
		c.App, c.Version = parts[0], parts[1]
	}
	switch {
	case f.Type != jobs.TypeRescan && f.Type != jobs.TypeUpdate && f.Type != jobs.TypeWinUpdate && f.Type != jobs.TypeReboot &&
		f.Type != campaign.TypeOSAll && f.Type != jobs.TypeUninstall && ((f.Type != jobs.TypeThirdParty && f.Type != campaign.TypeThirdPartyAll) || a.tpManifest == nil):
		return c, f, "Escolha o tipo: todas as atualizações do sistema, todos os programas aprovados, buscar atualizações, atualizar pacotes, instalar atualizações do Windows, reiniciar ou instalar programa de terceiros."
	case f.Type == jobs.TypeReboot && !containsInt(rebootDelays, f.Delay):
		return c, f, "Escolha o aviso do reinício da lista."
	case f.Close != 0 && !containsInt(rebootDelays, f.Close):
		return c, f, "Escolha o aviso antes de fechar o programa da lista."
	case f.Close != 0 && f.When == "prazo":
		return c, f, "Fechar o programa aberto só em horário fixo (de madrugada) ou na janela de cada prédio: com prazo, o programa seria fechado em pleno expediente."
	case f.Wake && f.When == "prazo":
		return c, f, "Ligar pela rede vai com a janela de cada prédio ou o horário fixo (de madrugada), não com o prazo."
	case f.Resend < 0 || f.Resend > campaign.MaxAutoResend:
		return c, f, "Escolha os reenvios automáticos da lista."
	case !containsInt(observeHours, f.ObserveH):
		return c, f, "Escolha a observação da lista."
	case f.When != "prazo" && f.When != "janela" && f.When != "horario":
		return c, f, "Escolha quando: com prazo, na janela de cada prédio ou em horário fixo."
	case f.When == "prazo" && !containsInt(ttlHours, f.TTLH):
		return c, f, "Escolha o prazo da lista."
	}
	if f.When == "horario" {
		start, err := window.ParseStart(f.DailyStart)
		if err != nil || !validDuration(f.DailyDur) {
			return c, f, "Horário fixo: a hora em HH:MM e a duração da lista."
		}
		c.Daily = &window.Window{Days: window.AllDays, Start: start, Duration: time.Duration(f.DailyDur) * time.Minute, Location: a.panelLocation()}
	}
	if f.Type == jobs.TypeUpdate {
		for _, s := range strings.FieldsFunc(f.Sources, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
			if !sourcePattern.MatchString(s) {
				return c, f, "Fontes: nomes de pacote fonte como aparecem nas pendências (ex.: curl, bluez), separados por vírgula."
			}
			if !containsStr(c.Sources, s) {
				c.Sources = append(c.Sources, s)
			}
		}
	} else if f.Type == jobs.TypeWinUpdate {
		// Aula 16.4: no Windows, o campo leva os KBs (KB5127648, ou só os dígitos).
		for _, s := range strings.FieldsFunc(f.Sources, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
			kb, ok := jobs.NormalizeKB(s)
			if !ok {
				return c, f, "KBs: como aparecem em \"Corrigido em\" (ex.: KB5127648), separados por vírgula."
			}
			if !containsStr(c.Sources, kb) {
				c.Sources = append(c.Sources, kb)
			}
		}
		if len(c.Sources) == 0 {
			return c, f, "Informe os KBs da campanha (ex.: KB5127648)."
		}
	}
	// Nos outros tipos, o campo de fontes está escondido (Aula 16.6): o que ficou nele de
	// antes de trocar o tipo é ignorado, como o programa e o aviso. A validação completa vem
	// com o escopo (parseCampaignForm).
	return c, f, ""
}

func containsInt(list []int, x int) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

func containsStr(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

type campaignConfirmView struct {
	pageBase
	Form            campaignForm
	TypeLabel, When string
	Scope           scopeConfirm
	SourcesList     string
	ProgramL        string
	Extra           string // reboot (Aula 16.5): o alvo, sem a coluna de previsão
	ObserveL        string
	Warnings        []string
	WakeL           string
	Changed         bool // o escopo mudou entre a conferência e a confirmação
}

// newCampaignAction: sem "confirmar", mostra a conferência; com ele, recalcula o escopo e
// cria (se o escopo mudou desde a conferência, mostra a conferência de novo).
func (a *api) newCampaignAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.jobKey == nil {
		http.Error(w, "o servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE)", http.StatusConflict)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	c, targets, f, problem := a.parseCampaignForm(r, o)
	// Terceiros (Aula 14.8): a mesma conferência da linha de comando e de cada etapa.
	if problem == "" && c.Type == jobs.TypeThirdParty {
		reason, err := a.campaigner().Ready(r.Context(), c.App, c.Version)
		if err != nil {
			a.internalError(w, r, "conferir a versão", err)
			return
		}
		if reason != "" {
			problem = "A versão não está pronta para campanha: " + reason + "."
		}
	}
	if c.Type == jobs.TypeUninstall && (problem != "" || r.PostFormValue("corrigir") == "1") {
		// A campanha de remoção nasce na página do programa: é lá que se corrige.
		back := programHref(f.RemoveOS, f.RemoveKey, f.RemovePub)
		if problem != "" {
			back += "&" + url.Values{"erro": {"campanha"}, "motivo": {problem}}.Encode()
		}
		http.Redirect(w, r, back+"#campanha", http.StatusSeeOther)
		return
	}
	if problem != "" || r.PostFormValue("corrigir") == "1" {
		v := campaignsView{pageBase: a.baseFor(r.Context(), s, "Campanhas", "campanhas"), CanNew: true}
		v.Error = problem
		status := http.StatusUnprocessableEntity
		if problem == "" {
			status = http.StatusOK // voltou da conferência para corrigir
		}
		f.Observe, f.TTL = hourOptions(observeHours, f.ObserveH), hourOptions(ttlHours, f.TTLH)
		if err := a.programChoices(r, &f); err != nil {
			a.internalError(w, r, "listar versões aprovadas", err)
			return
		}
		v.Form = f
		a.renderCampaigns(w, r, status, v)
		return
	}
	digest := scopeDigest(*c.Scope, targets)
	changed := r.PostFormValue("confirmar") == "1" && r.PostFormValue("escopo_hash") != digest
	if r.PostFormValue("confirmar") != "1" || changed {
		v, err := a.campaignConfirm(r, s, o, c, targets, f)
		if err != nil {
			a.internalError(w, r, "prever a campanha", err)
			return
		}
		v.Changed = changed
		a.render(w, r, http.StatusOK, "campanha-conferir", v)
		return
	}
	c.CreatedBy = s.Username
	id, err := a.store.CreateLocatedCampaign(r.Context(), c, targets)
	if err != nil {
		a.internalError(w, r, "criar campanha", err)
		return
	}
	a.logger.Info("campanha criada pelo painel", "campaign_id", id, "tipo", c.Type, "etapas", c.StagesText(false), "alvos", len(targets),
		"fontes", len(c.Sources), "programa", c.Target(), "janela", c.UseWindow, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "campanha.criar", fmt.Sprintf("campanha %d", id), texto.Clip(fmt.Sprintf("%s · %s · %d equipamento(s) · %s", c.Name, typeLabel(c.Type), len(targets), scopeAuditText(*c.Scope)), 2000))
	http.Redirect(w, r, fmt.Sprintf("/painel/campanhas/%d?criada=1", id), http.StatusSeeOther)
}

// campaignConfirm monta a conferência com o plano do servidor.
func (a *api) campaignConfirm(r *http.Request, s panel.Session, o *deployOverview, c campaign.Campaign, targets []campaign.Target, f campaignForm) (campaignConfirmView, error) {
	v := campaignConfirmView{pageBase: a.baseFor(r.Context(), s, "Conferir a campanha", "campanhas"), Form: f, TypeLabel: typeLabel(c.Type),
		SourcesList: strings.Join(c.Sources, ", "), ObserveL: hoursLabel(f.ObserveH)}
	v.Scope = buildScopeConfirm(o, *c.Scope, targets, f.scopeIn)
	groups := stageGroups(targets)
	var preview map[int]string
	var err error
	if c.Type == jobs.TypeUninstall {
		v.ProgramL = c.Target()
		if preview, err = a.removePreview(r, f, c, groups); err != nil {
			return v, err
		}
		v.Warnings = append(v.Warnings, "Na remoção, cada máquina da etapa recebe o job na hora em que a etapa for liberada, se o inventário dela mostrar o programa instalado para a máquina toda e com desinstalação silenciosa: o agente roda o desinstalador do próprio programa, sem nada na tela, e confere antes que a entrada ainda é dele. Instalações por usuário, sem desinstalação silenciosa e agentes antigos ficam de fora, com o motivo nas liberações. A coluna \"Hoje\" é a previsão de agora. Programa aberto: o desinstalador pode fechá-lo ou falhar; prefira a janela ou o horário fixo.")
	}
	if c.Type == jobs.TypeThirdParty {
		v.ProgramL = "programa: " + c.Target()
		if preview, err = a.thirdPartyPreview(r, c, groups); err != nil {
			return v, err
		}
		v.Warnings = append(v.Warnings, "No programa de terceiros, cada etapa recebe o plano da hora em que for liberada: a coluna \"Hoje\" é a previsão de agora. A aprovação e o arquivo no cache são conferidos de novo a cada etapa; revogada a aprovação, a campanha pausa.")
	}
	for i := range v.Scope.Stages {
		v.Scope.Stages[i].Preview = preview[i]
	}
	v.When = "com prazo de " + hoursLabel(f.TTLH) + " para cada job"
	if c.UseWindow {
		v.When = "na próxima janela de manutenção do prédio de cada equipamento"
	}
	if c.Daily != nil {
		v.When = "em horário fixo, " + c.Daily.String() + ": o horário da campanha vale para todos os prédios (não a janela de cada um); os jobs de cada etapa, quando ela for liberada, esperam o próximo horário"
	}
	if c.Scope.Pilot == nil {
		v.Warnings = append(v.Warnings, "Sem piloto: todos os equipamentos do escopo recebem na primeira liberação, cada um no horário que vale para ele.")
	}
	if len(v.Scope.MissingWindow) > 0 && !c.UseWindow {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d prédio(s) do escopo sem janela de manutenção ativa: com prazo ou horário fixo isso não impede a campanha.", len(v.Scope.MissingWindow)))
	}
	if v.Scope.UnlocatedN > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d equipamento(s) sem localização identificada incluídos explicitamente: não pertencem a nenhum prédio nem ao piloto.", v.Scope.UnlocatedN))
	}
	if len(v.Scope.Excluded) > 0 {
		v.Warnings = append(v.Warnings, "Equipamentos dos andares escolhidos com conflito de localização ficaram fora (entram só com o prédio inteiro).")
	}
	v.Warnings = append(v.Warnings, "O escopo fica gravado na criação: equipamentos novos nos prédios não entram depois; o que mudar de prédio (ou de andar, nos escolhidos por andar e no piloto por andar) não recebe job, com o motivo nas liberações. As pendências, aprovações e versões continuam conferidas na hora de cada liberação.")
	if c.Wake {
		v.WakeL = fmt.Sprintf("%d min antes da janela (ou do horário), só para as máquinas da etapa liberada", int(campaign.WakeLead/time.Minute))
		v.Warnings = append(v.Warnings, fmt.Sprintf("Ligar pela rede: %d min antes da janela do prédio (ou do horário fixo), o servidor pede a um agente ligado da mesma sub-rede o pacote mágico para cada máquina da etapa liberada com job da campanha e sem check-in há mais de %d min; as das etapas seguintes não são ligadas antes da liberação. Quando os jobs dela terminam, manda desligar só as que acordaram assim, e o agente só desliga sem ninguém conectado. Precisa do Wake on LAN pronto (aba Hardware › Ligar pela rede) e de um agente ligado em cada sub-rede.", int(campaign.WakeLead/time.Minute), int(campaign.WakeOffline/time.Minute)))
	}
	if c.Type == jobs.TypeUpdate {
		v.Warnings = append(v.Warnings, "No update, só recebe job quem tiver alguma dessas fontes pendente, com a correção aprovada em Patch, na hora em que a etapa for liberada.")
		approvals, err := a.store.PatchApprovals(r.Context())
		if err != nil {
			return v, err
		}
		for _, src := range c.Sources {
			if !slices.ContainsFunc(approvals, func(ap store.PatchApproval) bool { return ap.Item == src }) {
				v.Warnings = append(v.Warnings, fmt.Sprintf("A fonte %s não tem nenhuma correção aprovada em Patch: hoje, nenhuma máquina receberia essa fonte.", src))
			}
		}
	}
	if c.Type == jobs.TypeReboot {
		v.Extra = c.Target()
		v.Warnings = append(v.Warnings, "No reinício, só recebe job quem a busca atual mostra com reinício pendente; o agente confere de novo na hora (sem pendência, o job vira dispensado). Quem estiver conectado vê o aviso na tela pelo prazo escolhido e pode salvar o trabalho; um administrador na máquina pode cancelar.")
		if !c.UseWindow && c.Daily == nil {
			v.Warnings = append(v.Warnings, "Sem janela, cada máquina reinicia assim que receber o job, em qualquer horário: prefira \"na janela de cada prédio\" ou \"em horário fixo\".")
		}
		if c.Daily != nil {
			v.Warnings = append(v.Warnings, "Em horário fixo, a máquina precisa estar ligada e fazer check-in dentro do horário; o job vence no fim dele, como na janela.")
		}
	}
	if c.CloseDelay > 0 {
		v.Extra = fmt.Sprintf("programa aberto: fecha com aviso de %d min", int(c.CloseDelay/time.Minute))
		v.Warnings = append(v.Warnings, fmt.Sprintf("Programa aberto: no horário da campanha, quem estiver na sessão vê o aviso \"o programa será fechado em %d minutos para atualizar; salve seu trabalho\". Depois o agente pede para o programa fechar e, se ele não fechar em 2 minutos (uma pergunta de salvar sem resposta, por exemplo), fecha à força: o que não foi salvo se perde. Em sessão desconectada (ninguém vê o aviso), fecha sem esperar. Agentes antigos (sem a capacidade) e programas sem a lista de processos na regra continuam adiando, como hoje.", int(c.CloseDelay/time.Minute)))
	}
	if c.Type == jobs.TypeThirdParty || c.Type == campaign.TypeThirdPartyAll {
		v.Warnings = append(v.Warnings, "Programa aberto e o job adiado: assim que o programa fechar, o agente avisa e a nova tentativa sai em minutos (agentes novos); senão, de hora em hora, até 24.")
	}
	if c.MicrosoftFallback {
		v.Extra = "se a fonte da política falhar ou não oferecer o KB, direto da Microsoft"
		v.Warnings = append(v.Warnings, "Fonte reserva: quando a fonte da política (WSUS) falhar, ou não oferecer o KB, o job da máquina vai com a fonte Microsoft Update. Continua saindo só o que está aprovado em Patch; muda só de onde o arquivo vem (e o tráfego vai para a internet). Ficam de fora os agentes antigos (sem a fonte) e as máquinas com a GPO que bloqueia a internet do Windows Update.")
		if !a.microsoftAllowed() {
			v.Warnings = append(v.Warnings, "A fonte reserva está desligada em Configuração › Parâmetros: enquanto estiver, a opção não tem efeito e essas máquinas ficam sem job, como hoje.")
		}
	}
	if c.Type == jobs.TypeWinUpdate {
		v.Warnings = append(v.Warnings, "No Windows, cada máquina recebe, na hora em que a etapa for liberada, os KBs que corrigem alguma CVE dela aprovada em Patch e que a busca dela mostra pendentes na fonte da política (WSUS). Fonte falhando, KB só no catálogo offline ou fora da busca: fica sem job, com o motivo nas liberações (ou, com a fonte reserva, vai pela Microsoft). O job não reinicia: o reinício é pedido à parte.")
		approvals, err := a.store.PatchApprovals(r.Context())
		if err != nil {
			return v, err
		}
		for _, kb := range c.Sources {
			if !slices.ContainsFunc(approvals, func(ap store.PatchApproval) bool { return strings.Contains(ap.FixedIn, "("+kb+")") }) {
				v.Warnings = append(v.Warnings, fmt.Sprintf("O %s não tem nenhuma CVE aprovada em Patch: hoje, nenhuma máquina o receberia.", kb))
			}
		}
	}
	return v, nil
}

// --- Detalhe da campanha ------------------------------------------------------------

type campaignJobRow struct {
	ID                                   int64
	MachineID, Short, Name, State, Label string
	Detail, Slot                         string
}

type campaignRingView struct {
	Ring    string
	Title   string // "Grupo de implantação 0 (piloto)" na legada; o nome da etapa na por localização
	Current bool
	Status  string // "ainda não liberado", "nenhuma máquina com o que fazer", ""
	Jobs    []campaignJobRow
	// Os contadores por estado (cada um filtra a tabela), o total e o link para ver a tabela
	// deste grupo (só o grupo escolhido mostra os jobs).
	Counts   []campaignCount
	Total    int
	Href     string
	Selected bool
	HasSlot  bool // campanha com um job por programa: a coluna Programa
	// A tabela filtrada e paginada.
	Found      int
	Shown      string
	Prev, Next string
	Q, Filter  string
	Slot       string
	Slots      []optView
	Idx        int
	CampaignID int64
	CSV        string // o CSV deste grupo, com o filtro da tela
}

type campaignCount struct {
	State, Label, Href string
	N                  int
	Active             bool
}

// campaignJobsPerPage: os jobs mostrados por página no detalhe da campanha.
const campaignJobsPerPage = 100

type campaignView struct {
	pageBase
	C                                   campaign.Campaign
	TypeLabel, State, StateLabel, When  string
	Fallback                            string // a fonte reserva da Microsoft, quando a campanha a usa
	WakeL                               string // ligar pela rede na janela (Aula 22.3): o resumo
	Wakes                               []wakeRow
	CloseL                              string // terceiros: fechar o programa aberto com aviso
	Sources, Observe, Created           string
	Rings                               []campaignRingView
	CanPause, CanResume, CanCancel      bool
	CanArchive, CanUnarchive, CanDelete bool
	ResendL                             string         // os reenvios automáticos
	Resend                              []resendOption // o que dá para reenviar no grupo atual (admin)
	// A campanha por localização: as etapas, o piloto e o progresso por prédio (e andar).
	Located             bool
	StagesL, PilotL     string
	Progress            []campaignProgressRow
	TotalTargets, DoneN int
	PilotTotal, PilotOK int
	WaitWindowN, WaitPN int
	ScopeL              string
}

// campaignProgressRow: o andamento de um prédio (ou do andar piloto) numa campanha por localização.
type campaignProgressRow struct {
	Stage, Name, Next                                      string
	Total, Done, Running, WaitWindow, WaitPilot, Postponed int
	Failed, Dismissed, NoJob                               int
}

func campaignID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (a *api) campaignPage(w http.ResponseWriter, r *http.Request) {
	v := campaignView{pageBase: a.base(r, "Campanha", "campanhas")}
	id, ok := campaignID(r)
	var c campaign.Campaign
	var err error
	if ok {
		c, ok, err = a.store.Campaign(r.Context(), id)
		if err != nil {
			a.internalError(w, r, "ler campanha", err)
			return
		}
	}
	if !ok {
		a.render(w, r, http.StatusNotFound, "campanha-nao-encontrada", v.pageBase)
		return
	}
	v.C, v.Title = c, fmt.Sprintf("Campanha %d", c.ID)
	v.TypeLabel, v.State, v.StateLabel = typeLabel(c.Type), c.State, campaignLabel(c.State)
	v.When = "prazo de " + hoursLabel(int(c.TTL.Hours())) + " por job"
	if c.UseWindow {
		v.When = "na janela de cada grupo de implantação"
		if c.Located() {
			v.When = "na janela de manutenção do prédio de cada equipamento"
		}
	}
	if c.Daily != nil {
		v.When = "em horário fixo, " + c.Daily.String()
	}
	if c.CloseDelay > 0 {
		v.CloseL = fmt.Sprintf("fecha com aviso de %d min (no horário da campanha)", int(c.CloseDelay/time.Minute))
	}
	v.ResendL = "nenhum: a primeira falha conta"
	if c.AutoResend > 0 {
		v.ResendL = fmt.Sprintf("até %d vez(es) por máquina: o expirado vai de novo na hora; o que falhou, depois de 1 h", c.AutoResend)
	}
	if c.MicrosoftFallback {
		v.Fallback = "se a fonte da política falhar ou não oferecer o KB, direto da Microsoft Update"
	}
	if c.Wake {
		if err := a.campaignWakeView(r, c, &v); err != nil {
			a.internalError(w, r, "ler o ligar pela rede", err)
			return
		}
	}
	v.Sources, v.Observe = dash(c.Target()), hoursLabel(int(c.Observe.Hours()))
	v.Created = a.stamp(c.CreatedAt)
	if v.Admin {
		v.CanPause = c.State == campaign.StateRunning
		v.CanResume = c.State == campaign.StatePaused
		v.CanCancel = c.State == campaign.StateRunning || c.State == campaign.StatePaused
		v.CanArchive = c.Finished() && c.ArchivedAt == nil
		v.CanUnarchive = c.ArchivedAt != nil
		v.CanDelete = c.Finished() && c.ArchivedAt != nil
		if v.CanCancel && c.LaunchedIdx == c.CurrentIdx {
			list, err := a.store.CampaignJobs(r.Context(), c.ID, c.CurrentIdx)
			if err != nil {
				a.internalError(w, r, "listar jobs da campanha", err)
				return
			}
			by := campaign.FailedByReason(list, a.now(), c.AutoResend)
			for _, reason := range campaign.ResendReasons {
				if n := by[reason]; n > 0 {
					v.Resend = append(v.Resend, resendOption{Value: reason, Label: resendLabel(reason), N: n})
				}
			}
		}
	}
	campaignFlash(r, &v.pageBase)

	names := map[string]string{}
	if ms, err := a.store.Machines(r.Context()); err == nil {
		for _, m := range ms {
			names[m.ID] = m.Hostname
		}
	}
	// O grupo cuja tabela aparece (?grupo=, a posição na campanha; padrão: o atual) e o
	// filtro dela: estado, máquina (nome ou ID) e programa, com páginas de 100.
	q := r.URL.Query()
	sel := min(max(c.CurrentIdx, 0), c.StageCount()-1)
	if n, err := strconv.Atoi(q.Get("grupo")); err == nil && n >= 0 && n < c.StageCount() {
		sel = n
	}
	stageJobs := make([][]campaign.JobState, c.StageCount())
	for idx := range stageJobs {
		if stageJobs[idx], err = a.store.CampaignJobs(r.Context(), c.ID, idx); err != nil {
			a.internalError(w, r, "listar jobs da campanha", err)
			return
		}
	}
	if c.Located() {
		if err := a.campaignProgress(r, c, stageJobs, &v); err != nil {
			a.internalError(w, r, "ler o andamento por prédio", err)
			return
		}
	}
	filter, needle, slot := q.Get("estado"), strings.ToLower(strings.TrimSpace(texto.Clip(q.Get("q"), 100))), texto.Clip(q.Get("programa"), 64)
	page, _ := strconv.Atoi(q.Get("pagina"))
	page = max(page, 1)
	base := fmt.Sprintf("/painel/campanhas/%d", c.ID)
	href := func(idx int, estado, busca, prog string, pg int) string {
		u := url.Values{"grupo": {strconv.Itoa(idx)}}
		for k, v := range map[string]string{"estado": estado, "q": busca, "programa": prog} {
			if v != "" {
				u.Set(k, v)
			}
		}
		if pg > 1 {
			u.Set("pagina", strconv.Itoa(pg))
		}
		return base + "?" + u.Encode() + "#grupo-" + strconv.Itoa(idx)
	}
	for idx := range c.StageCount() {
		rv := campaignRingView{Current: idx == c.CurrentIdx && c.State != campaign.StateDone,
			Idx: idx, CampaignID: c.ID, Selected: idx == sel, Href: href(idx, "", "", "", 1)}
		if c.Located() {
			rv.Ring, rv.Title = c.StageLabel(idx), "Etapa "+strconv.Itoa(idx+1)+": "+c.StageLabel(idx)
		} else {
			rv.Ring = ringLabel(c.Rings[idx])
			rv.Title = "Grupo de implantação " + rv.Ring
		}
		list := stageJobs[idx]
		switch {
		case idx > c.LaunchedIdx && c.PilotStage(c.CurrentIdx):
			rv.Status = "aguardando a liberação do piloto"
		case idx > c.LaunchedIdx:
			rv.Status = "ainda não liberado"
		case len(list) == 0:
			rv.Status = "nenhuma máquina com o que fazer"
		}
		rv.Total = len(list)
		counts, slots := map[string]int{}, map[string]bool{}
		for _, j := range list {
			counts[j.State]++
			if j.Slot != "" {
				slots[j.Slot] = true
			}
		}
		rv.HasSlot = len(slots) > 0
		for _, st := range []string{jobs.StatePending, jobs.StateDelivered, jobs.StateDone, jobs.StatePostponed, jobs.StateFailed,
			jobs.StateRejected, jobs.StateDismissed, jobs.StateExpired, jobs.StateCanceled} {
			if counts[st] == 0 {
				continue
			}
			active := rv.Selected && filter == st
			next := st
			if active {
				next = "" // clicar no ativo tira o filtro
			}
			rv.Counts = append(rv.Counts, campaignCount{State: st, Label: stateLabel(st), N: counts[st], Active: active,
				Href: href(idx, next, needle, slot, 1)})
		}
		if !rv.Selected {
			v.Rings = append(v.Rings, rv)
			continue
		}
		rv.Q, rv.Filter, rv.Slot = needle, filter, slot
		for _, sl := range slices.Sorted(maps.Keys(slots)) {
			rv.Slots = append(rv.Slots, optView{Value: sl, Label: sl, Selected: sl == slot})
		}
		var found []campaign.JobState
		for _, j := range list {
			if campaignJobMatch(j, filter, slot, needle, names) {
				found = append(found, j)
			}
		}
		rv.Found = len(found)
		rv.CSV = campaignCSVHref(c.ID, idx, filter, needle, slot)
		pages := max(1, (len(found)+campaignJobsPerPage-1)/campaignJobsPerPage)
		pg := min(page, pages)
		from, to := (pg-1)*campaignJobsPerPage, min(pg*campaignJobsPerPage, len(found))
		if len(found) > 0 {
			rv.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(found))
		}
		if pg > 1 {
			rv.Prev = href(idx, filter, needle, slot, pg-1)
		}
		if pg < pages {
			rv.Next = href(idx, filter, needle, slot, pg+1)
		}
		for _, j := range found[from:to] {
			rv.Jobs = append(rv.Jobs, campaignJobRow{ID: j.ID, MachineID: j.MachineID, Short: short(j.MachineID),
				Name: dash(names[j.MachineID]), State: j.State, Label: stateLabel(j.State), Detail: dash(wuerr.Annotate(j.Detail)), Slot: dash(j.Slot)})
		}
		v.Rings = append(v.Rings, rv)
	}
	a.render(w, r, http.StatusOK, "campanha", v)
}

// campaignJobMatch: o filtro da tabela de um grupo (estado, programa, máquina por nome ou
// começo do ID), o mesmo na tela e no CSV.
func campaignJobMatch(j campaign.JobState, filter, slot, needle string, names map[string]string) bool {
	switch {
	case filter != "" && j.State != filter:
		return false
	case slot != "" && j.Slot != slot:
		return false
	case needle != "" && !strings.Contains(strings.ToLower(names[j.MachineID]), needle) && !strings.HasPrefix(j.MachineID, needle):
		return false
	}
	return true
}

// campaignCSVHref: o CSV de um grupo com o filtro da tela.
func campaignCSVHref(id int64, idx int, estado, busca, prog string) string {
	u := url.Values{"grupo": {strconv.Itoa(idx)}}
	for k, v := range map[string]string{"estado": estado, "q": busca, "programa": prog} {
		if v != "" {
			u.Set(k, v)
		}
	}
	return fmt.Sprintf("/painel/campanhas/%d/resultado.csv?%s", id, u.Encode())
}

// campaignCSV: GET /painel/campanhas/{id}/resultado.csv, o resultado da campanha, uma linha
// por job. Sem ?grupo=, todos os grupos de implantação; com ele, só aquele, com o mesmo
// filtro da tabela da tela (estado, q, programa).
func (a *api) campaignCSV(w http.ResponseWriter, r *http.Request) {
	id, ok := campaignID(r)
	var c campaign.Campaign
	var err error
	if ok {
		c, ok, err = a.store.Campaign(r.Context(), id)
		if err != nil {
			a.internalError(w, r, "ler campanha", err)
			return
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	idxs := make([]int, 0, c.StageCount())
	for i := range c.StageCount() {
		idxs = append(idxs, i)
	}
	name := fmt.Sprintf("patchd-campanha-%d.csv", c.ID)
	if q.Has("grupo") {
		n, err := strconv.Atoi(q.Get("grupo"))
		if err != nil || n < 0 || n >= c.StageCount() {
			http.NotFound(w, r)
			return
		}
		idxs = []int{n}
		if c.Located() {
			name = fmt.Sprintf("patchd-campanha-%d-etapa-%d.csv", c.ID, n+1)
		} else {
			name = fmt.Sprintf("patchd-campanha-%d-grupo-%d.csv", c.ID, c.Rings[n])
		}
	}
	where := map[string]campaign.Target{}
	if c.Located() {
		ts, err := a.store.CampaignTargets(r.Context(), c.ID, -1)
		if err != nil {
			a.internalError(w, r, "listar os alvos da campanha", err)
			return
		}
		for _, t := range ts {
			where[t.MachineID] = t
		}
	}
	filter, needle, slot := q.Get("estado"), strings.ToLower(strings.TrimSpace(texto.Clip(q.Get("q"), 100))), texto.Clip(q.Get("programa"), 64)
	names := map[string]string{}
	ms, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	for _, m := range ms {
		names[m.ID] = m.Hostname
	}
	header := []string{"campanha", "etapa", "grupo_de_implantacao", "predio", "andar", "job", "maquina", "nome", "programa", "estado", "detalhe",
		"janela_utc", "encerrado_utc"}
	var rows [][]string
	for _, idx := range idxs {
		list, err := a.store.CampaignJobs(r.Context(), c.ID, idx)
		if err != nil {
			a.internalError(w, r, "listar jobs da campanha", err)
			return
		}
		for _, j := range list {
			if !campaignJobMatch(j, filter, slot, needle, names) {
				continue
			}
			window, finished := "", ""
			if j.NotBefore != nil {
				window = export.When(*j.NotBefore)
			}
			if j.FinishedAt != nil {
				finished = export.When(*j.FinishedAt)
			}
			ring, building, floor := "", "", ""
			if c.Located() {
				t := where[j.MachineID]
				building, floor = texto.PlaceName(t.Building), t.Floor
				if t.Via == campaign.ViaUnlocated {
					building = "sem localização"
				}
			} else {
				ring = ringLabel(c.Rings[idx])
			}
			rows = append(rows, []string{strconv.FormatInt(c.ID, 10), c.StageLabel(idx), ring, building, floor, strconv.FormatInt(j.ID, 10),
				j.MachineID, names[j.MachineID], j.Slot, stateLabel(j.State), wuerr.Annotate(j.Detail), window, finished})
		}
	}
	a.sendCSV(w, r, name, len(rows), func(b *bytes.Buffer) error { return export.Table(b, header, rows) })
}

func campaignFlash(r *http.Request, b *pageBase) {
	q := r.URL.Query()
	switch {
	case q.Has("criada"):
		b.Flash = "Campanha criada: o servidor libera a primeira etapa no próximo minuto."
	case q.Get("feito") == "pausar":
		b.Flash = "Campanha pausada: nenhuma etapa nova é liberada; os jobs já criados seguem."
	case q.Get("feito") == "retomar":
		b.Flash = "Campanha retomada: as falhas (ou a falta de evidência do piloto) da etapa atual ficam aceitas, e ela segue."
	case q.Get("feito") == "reenviar":
		b.Flash = "Reenvio pedido: no próximo minuto o servidor refaz o plano de cada máquina e manda os jobs de novo (a que já não precisa sai como dispensada). O que falhar de novo pausa a campanha de novo."
	case q.Get("feito") == "desarquivar":
		b.Flash = "Campanha desarquivada: ela volta para a lista."
	case q.Get("erro") == "confirmar":
		b.Error = "Para excluir, marque a confirmação."
	case q.Get("erro") == "reenvio-vazio":
		b.Error = "Marque o que reenviar."
	case q.Get("feito") == "cancelar":
		n, _ := strconv.Atoi(q.Get("jobs"))
		b.Flash = fmt.Sprintf("Campanha cancelada; %d job(s) pendente(s) cancelado(s). Os já entregues seguem: o agente pode estar executando.", n)
	case q.Get("erro") == "estado":
		b.Error = "A campanha não está num estado que permita isso (ela pode ter mudado agora há pouco)."
	case q.Get("erro") == "mudou":
		b.Error = "A campanha mudou de estado enquanto você decidia (o servidor avança as campanhas a cada minuto). Confira e tente de novo."
	}
}

// campaignChangeAction: pausar, retomar ou cancelar, pelas regras de campaign.Change.
func (a *api) campaignChangeAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, ok := campaignID(r)
	ops := map[string]string{"pausar": campaign.OpPause, "retomar": campaign.OpResume, "cancelar": campaign.OpCancel, "reenviar": "reenviar",
		"arquivar": "arquivar", "desarquivar": "desarquivar", "excluir": "excluir"}
	op, known := ops[r.PathValue("op")]
	if !ok || !known {
		http.NotFound(w, r)
		return
	}
	c, found, err := a.store.Campaign(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler campanha", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	switch op {
	case "reenviar":
		a.campaignResendAction(w, r, s, c)
		return
	case "arquivar", "desarquivar", "excluir":
		a.campaignArchiveAction(w, r, s, c, op)
		return
	}
	back := fmt.Sprintf("/painel/campanhas/%d", id)
	n, err := campaign.Change(r.Context(), a.store, c, op, s.Username)
	switch {
	case errors.Is(err, campaign.ErrState):
		http.Redirect(w, r, back+"?erro=estado", http.StatusSeeOther)
		return
	case errors.Is(err, campaign.ErrChanged):
		http.Redirect(w, r, back+"?erro=mudou", http.StatusSeeOther)
		return
	case err != nil:
		a.internalError(w, r, "mudar campanha", err)
		return
	}
	a.logger.Info("campanha alterada pelo painel", "campaign_id", id, "op", op, "jobs_cancelados", n,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "campanha.alterar", fmt.Sprintf("campanha %d", id), fmt.Sprintf("%s · %d job(s) cancelado(s)", r.PathValue("op"), n))
	http.Redirect(w, r, fmt.Sprintf("%s?feito=%s&jobs=%d", back, r.PathValue("op"), n), http.StatusSeeOther)
}

var resendChoices = []int{0, 1, 2, 3, 4, 5}

type resendOption struct {
	Value, Label string
	N            int
}

func resendLabel(reason string) string {
	switch reason {
	case campaign.ReasonExpired:
		return "expirados (a máquina não pegou o job no prazo: desligada, fora da rede)"
	case campaign.ReasonFailed:
		return "que falharam (o download ou o instalador deu erro)"
	case campaign.ReasonRejected:
		return "recusados pelo agente (assinatura, sistema, versão do agente): tendem a recusar de novo"
	case campaign.ReasonPostponed:
		return "adiados até o limite de tentativas (programa sempre aberto): mais uma tentativa"
	}
	return reason
}

// campaignResendAction: POST /painel/campanhas/{id}/reenviar. O motor reenvia no próximo minuto.
func (a *api) campaignResendAction(w http.ResponseWriter, r *http.Request, s panel.Session, c campaign.Campaign) {
	_ = r.ParseForm()
	back := fmt.Sprintf("/painel/campanhas/%d", c.ID)
	reasons := r.PostForm["motivo"]
	if len(reasons) == 0 {
		http.Redirect(w, r, back+"?erro=reenvio-vazio", http.StatusSeeOther)
		return
	}
	err := campaign.RequestResend(r.Context(), a.store, c, reasons, s.Username)
	switch {
	case errors.Is(err, campaign.ErrState):
		http.Redirect(w, r, back+"?erro=estado", http.StatusSeeOther)
		return
	case errors.Is(err, campaign.ErrChanged):
		http.Redirect(w, r, back+"?erro=mudou", http.StatusSeeOther)
		return
	case err != nil:
		http.Error(w, "pedido inválido", http.StatusBadRequest)
		return
	}
	a.audit(r, s, "campanha.reenviar", fmt.Sprintf("campanha %d", c.ID), "motivos: "+strings.Join(reasons, ", "))
	http.Redirect(w, r, back+"?feito=reenviar", http.StatusSeeOther)
}

// campaignArchiveAction: arquivar, desarquivar ou excluir uma campanha (só a encerrada; a
// exclusão, só a arquivada e com a confirmação).
func (a *api) campaignArchiveAction(w http.ResponseWriter, r *http.Request, s panel.Session, c campaign.Campaign, op string) {
	back := fmt.Sprintf("/painel/campanhas/%d", c.ID)
	var n int
	var err error
	switch op {
	case "arquivar":
		n, err = a.store.ArchiveCampaigns(r.Context(), []int64{c.ID}, true, a.now())
	case "desarquivar":
		n, err = a.store.ArchiveCampaigns(r.Context(), []int64{c.ID}, false, a.now())
	case "excluir":
		if r.PostFormValue("confirmo") != "1" {
			http.Redirect(w, r, back+"?erro=confirmar", http.StatusSeeOther)
			return
		}
		var ok bool
		ok, err = a.store.DeleteCampaign(r.Context(), c.ID)
		if ok {
			n = 1
		}
	}
	if err != nil {
		a.internalError(w, r, op+" campanha", err)
		return
	}
	if n == 0 {
		http.Redirect(w, r, back+"?erro=estado", http.StatusSeeOther)
		return
	}
	a.audit(r, s, "campanha."+op, fmt.Sprintf("campanha %d", c.ID), fmt.Sprintf("%s (%s, %s)", c.Name, campaignLabel(c.State), typeLabel(c.Type)))
	switch op {
	case "excluir":
		http.Redirect(w, r, fmt.Sprintf("/painel/campanhas?arquivadas=1&excluida=%d", c.ID), http.StatusSeeOther)
	case "arquivar":
		http.Redirect(w, r, fmt.Sprintf("/painel/campanhas?arquivada=%d", c.ID), http.StatusSeeOther)
	default:
		http.Redirect(w, r, back+"?feito=desarquivar", http.StatusSeeOther)
	}
}

// archiveFinishedAction: POST /painel/campanhas/arquivar-encerradas. Arquiva de uma vez as
// concluídas e canceladas que ainda estão na lista.
func (a *api) archiveFinishedAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	list, err := a.store.Campaigns(r.Context(), campaignsScan)
	if err != nil {
		a.internalError(w, r, "listar campanhas", err)
		return
	}
	var ids []int64
	var keys []string
	for _, c := range list {
		if c.Finished() && c.ArchivedAt == nil {
			ids = append(ids, c.ID)
			keys = append(keys, strconv.FormatInt(c.ID, 10))
		}
	}
	n := 0
	if len(ids) > 0 {
		if n, err = a.store.ArchiveCampaigns(r.Context(), ids, true, a.now()); err != nil {
			a.internalError(w, r, "arquivar campanhas", err)
			return
		}
		a.audit(r, s, "campanha.arquivar-encerradas", fmt.Sprintf("%d campanha(s)", n), texto.Clip("ids: "+strings.Join(keys, ","), 2000))
	}
	http.Redirect(w, r, fmt.Sprintf("/painel/campanhas?arquivadas-agora=%d", n), http.StatusSeeOther)
}

type wakeRow struct {
	MachineID, Name, Ring, Wake, Woke, Shutdown string
}

// campaignWakeView: o resumo e a tabela do ligar e desligar (Aula 22.3), de todos os grupos.
func (a *api) campaignWakeView(r *http.Request, c campaign.Campaign, v *campaignView) error {
	recs, err := a.store.CampaignWakes(r.Context(), c.ID, -1)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		v.WakeL = "ligada: nenhuma máquina precisou ainda (o pacote sai minutos antes da janela, para as desligadas)"
		return nil
	}
	names, err := a.machineNames(r.Context())
	if err != nil {
		return err
	}
	var asked, notAsked, woke, off, stayed int
	for _, w := range recs {
		row := wakeRow{MachineID: w.MachineID, Name: dash(names[w.MachineID]), Ring: c.StageLabel(w.Idx)}
		if !c.Located() {
			row.Ring = strconv.Itoa(c.Rings[min(w.Idx, len(c.Rings)-1)])
		}
		switch {
		case w.WakeRequestID == 0:
			notAsked++
			row.Wake, row.Woke = "não saiu: "+w.Problem, "—"
		case w.AnsweredAt != nil:
			asked++
			woke++
			row.Wake = "pedido " + a.stamp(w.RequestedAt)
			row.Woke = "acordou " + lapse(w.AnsweredAt.Sub(w.RequestedAt)) + " depois do pedido"
		default:
			asked++
			row.Wake = "pedido " + a.stamp(w.RequestedAt)
			row.Woke = "ainda não"
		}
		switch {
		case w.ShutdownJobID != 0 && strings.HasPrefix(w.ShutdownDetail, "desligando"):
			off++
			row.Shutdown = "desligada (job " + strconv.FormatInt(w.ShutdownJobID, 10) + ")"
		case w.ShutdownJobID != 0 && w.ShutdownDetail != "":
			stayed++
			row.Shutdown = w.ShutdownDetail
		case w.ShutdownJobID != 0:
			row.Shutdown = "job " + strconv.FormatInt(w.ShutdownJobID, 10) + " " + stateLabel(w.ShutdownState)
		case w.ShutdownNote != "":
			row.Shutdown = w.ShutdownNote
		default:
			row.Shutdown = "depois dos jobs dela"
		}
		v.Wakes = append(v.Wakes, row)
	}
	v.WakeL = fmt.Sprintf("%d pedido(s) de ligar, %d acordaram, %d desligadas depois, %d ficaram ligadas (alguém conectado); %d sem pedido (sem repetidor, sem MAC...)",
		asked, woke, off, stayed, notAsked)
	return nil
}
