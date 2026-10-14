package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// As campanhas no painel. Aula 8.5: a lista, só leitura. Aula 11.2: o admin cria (com uma
// tela de conferência antes, porque a campanha vira jobs em máquinas de verdade no minuto
// seguinte), vê o andamento de cada anel e pausa, retoma ou cancela, com as mesmas regras
// da linha de comando (campaign.Change).

const campaignsShown = 50

// As opções do formulário: listas fechadas, como na janela dos anéis.
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
}

// ringChoice é um anel no formulário, com o que o admin precisa saber antes de marcar.
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
	Rings                     []ringChoice
	Observe, TTL              []hourOption
	ObserveH, TTLH            int
	Chosen                    []int // anéis marcados, em ordem
}

type campaignsView struct {
	pageBase
	Rows   []campaignRow
	CanNew bool // admin e chave de jobs
	NoKey  bool // admin sem a chave: o formulário não aparece
	Form   campaignForm
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

// ringChoices monta os anéis 0..9 com quantas máquinas e a janela de cada um.
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
	if v.CanNew {
		f := campaignForm{Type: jobs.TypeRescan, When: "prazo", ObserveH: 24, TTLH: 24}
		// Vindo de "Campanha por anel" na página do programa (Aula 14.8): já preenchido.
		if q := r.URL.Query(); q.Get("tipo") == jobs.TypeThirdParty {
			f.Type, f.Program = jobs.TypeThirdParty, q.Get("programa")
			f.Name = f.Program
		}
		var err error
		if f.Rings, err = a.ringChoices(r, nil); err != nil {
			a.internalError(w, r, "listar anéis", err)
			return
		}
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
	list, err := a.store.Campaigns(r.Context(), campaignsShown)
	if err != nil {
		a.internalError(w, r, "listar campanhas", err)
		return
	}
	for _, c := range list {
		typ := typeLabel(c.Type)
		if c.UseWindow {
			typ += ", na janela"
		}
		v.Rows = append(v.Rows, campaignRow{ID: c.ID, Name: c.Name, Type: typ, Sources: dash(c.Target()),
			Rings: ringsProgress(c), State: c.State, Label: campaignLabel(c.State), Detail: dash(c.Detail),
			By: c.CreatedBy, Created: ago(a.now().Sub(c.CreatedAt))})
	}
	a.render(w, r, status, "campanhas", v)
}

// ringsProgress: "[0] → 1", com o anel atual entre colchetes.
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

// parseCampaignForm lê e confere o formulário. A mensagem de erro é para a tela.
func (a *api) parseCampaignForm(r *http.Request) (campaign.Campaign, campaignForm, string) {
	_ = r.ParseForm()
	f := campaignForm{Name: strings.TrimSpace(r.PostFormValue("nome")), Type: r.PostFormValue("tipo"),
		Sources: strings.TrimSpace(r.PostFormValue("fontes")), When: r.PostFormValue("quando"), Program: r.PostFormValue("programa")}
	f.ObserveH, _ = strconv.Atoi(r.PostFormValue("observacao"))
	f.TTLH, _ = strconv.Atoi(r.PostFormValue("prazo"))
	seen := map[int]bool{}
	for _, s := range r.PostForm["anel"] {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > store.MaxRing || seen[n] {
			return campaign.Campaign{}, f, fmt.Sprintf("Anéis de 0 a %d, cada um uma vez.", store.MaxRing)
		}
		seen[n] = true
		f.Chosen = append(f.Chosen, n)
	}
	sort.Ints(f.Chosen) // a ordem é sempre crescente: o piloto primeiro
	c := campaign.Campaign{Name: f.Name, Type: f.Type, Rings: f.Chosen, Observe: time.Duration(f.ObserveH) * time.Hour,
		TTL: time.Duration(f.TTLH) * time.Hour, UseWindow: f.When == "janela"}
	if f.Type == jobs.TypeThirdParty {
		// O select manda "programa versão"; nos outros tipos ele vai junto e é ignorado.
		parts := strings.Fields(f.Program)
		if len(parts) != 2 {
			return c, f, "Escolha o programa e a versão aprovada."
		}
		c.App, c.Version = parts[0], parts[1]
	}
	switch {
	case f.Type != jobs.TypeRescan && f.Type != jobs.TypeUpdate && (f.Type != jobs.TypeThirdParty || a.tpManifest == nil):
		return c, f, "Escolha o tipo: buscar atualizações, atualizar pacotes ou instalar programa de terceiros."
	case len(f.Chosen) == 0:
		return c, f, "Marque ao menos um anel."
	case !containsInt(observeHours, f.ObserveH):
		return c, f, "Escolha a observação da lista."
	case f.When != "prazo" && f.When != "janela":
		return c, f, "Escolha quando: com prazo ou na janela de cada anel."
	case f.When == "prazo" && !containsInt(ttlHours, f.TTLH):
		return c, f, "Escolha o prazo da lista."
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
	} else if f.Sources != "" {
		return c, f, "Só a campanha de atualizar pacotes leva fontes."
	}
	if err := c.Validate(); err != nil {
		return c, f, "Campanha inválida: " + err.Error() + "."
	}
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

// confirmRing é uma linha da conferência.
type confirmRing struct {
	Label, Window, Note string
	Preview             string // thirdparty: o que o anel receberia agora (Aula 14.8)
	Machines            int
}

type campaignConfirmView struct {
	pageBase
	Form            campaignForm
	TypeLabel, When string
	Rings           []confirmRing
	SourcesList     string
	ProgramL        string
	ObserveL        string
	Warnings        []string
	ChosenCSV       string
}

// newCampaignAction: sem "confirmar", mostra a conferência; com ele, cria.
func (a *api) newCampaignAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.jobKey == nil {
		http.Error(w, "o servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE)", http.StatusConflict)
		return
	}
	c, f, problem := a.parseCampaignForm(r)
	choices, err := a.ringChoices(r, f.Chosen)
	if err != nil {
		a.internalError(w, r, "listar anéis", err)
		return
	}
	// Terceiros (Aula 14.8): a mesma conferência da linha de comando e de cada anel.
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
	if problem == "" && c.UseWindow {
		var missing []string
		for _, rc := range choices {
			if rc.Checked && rc.Window == "" {
				missing = append(missing, strconv.Itoa(rc.Ring))
			}
		}
		if len(missing) > 0 {
			problem = "Anel sem janela de manutenção: " + strings.Join(missing, ", ") + ". Defina a janela em Anéis ou escolha \"com prazo\"."
		}
	}
	if problem != "" || r.PostFormValue("corrigir") == "1" {
		v := campaignsView{pageBase: a.baseFor(s, "Campanhas", "campanhas"), CanNew: true}
		v.Error = problem
		status := http.StatusUnprocessableEntity
		if problem == "" {
			status = http.StatusOK // voltou da conferência para corrigir
		}
		f.Rings = choices
		f.Observe, f.TTL = hourOptions(observeHours, f.ObserveH), hourOptions(ttlHours, f.TTLH)
		if err := a.programChoices(r, &f); err != nil {
			a.internalError(w, r, "listar versões aprovadas", err)
			return
		}
		v.Form = f
		a.renderCampaigns(w, r, status, v)
		return
	}
	if r.PostFormValue("confirmar") != "1" {
		v := campaignConfirmView{pageBase: a.baseFor(s, "Conferir a campanha", "campanhas"), Form: f, TypeLabel: typeLabel(c.Type),
			SourcesList: strings.Join(c.Sources, ", "), ObserveL: hoursLabel(f.ObserveH)}
		var preview map[int]string
		if c.Type == jobs.TypeThirdParty {
			v.ProgramL = "programa: " + c.Target()
			if preview, err = a.thirdPartyPreview(r, c); err != nil {
				a.internalError(w, r, "prever a campanha", err)
				return
			}
			v.Warnings = append(v.Warnings, "No programa de terceiros, cada anel recebe o plano da hora em que for liberado: a coluna \"Hoje\" é a previsão de agora. A aprovação e o arquivo no cache são conferidos de novo a cada anel; revogada a aprovação, a campanha pausa.")
		}
		v.When = "com prazo de " + hoursLabel(f.TTLH) + " para cada job"
		if c.UseWindow {
			v.When = "na próxima janela de manutenção de cada anel"
		}
		chosen := make([]string, len(f.Chosen))
		for i, x := range f.Chosen {
			chosen[i] = strconv.Itoa(x)
		}
		v.ChosenCSV = strings.Join(chosen, ",")
		for _, rc := range choices {
			if !rc.Checked {
				continue
			}
			cr := confirmRing{Label: rc.Label, Machines: rc.Machines, Window: dash(rc.Window), Preview: preview[rc.Ring]}
			if rc.Machines == 0 {
				cr.Note = "nenhuma máquina: o anel passa sem jobs"
				v.Warnings = append(v.Warnings, fmt.Sprintf("O anel %d não tem máquinas.", rc.Ring))
			}
			v.Rings = append(v.Rings, cr)
		}
		if f.Chosen[0] != store.PilotRing {
			v.Warnings = append(v.Warnings, "A campanha não começa pelo piloto (anel 0).")
		}
		if c.Type == jobs.TypeUpdate {
			v.Warnings = append(v.Warnings, "No update, só recebe job quem tiver alguma dessas fontes pendente, com a correção aprovada em Patch, na hora em que o anel for liberado.")
			approvals, err := a.store.PatchApprovals(r.Context())
			if err != nil {
				a.internalError(w, r, "listar aprovações", err)
				return
			}
			for _, src := range c.Sources {
				if !slices.ContainsFunc(approvals, func(ap store.PatchApproval) bool { return ap.Item == src }) {
					v.Warnings = append(v.Warnings, fmt.Sprintf("A fonte %s não tem nenhuma correção aprovada em Patch: hoje, nenhuma máquina receberia essa fonte.", src))
				}
			}
		}
		a.render(w, r, http.StatusOK, "campanha-conferir", v)
		return
	}
	c.CreatedBy = s.Username
	id, err := a.store.CreateCampaign(r.Context(), c)
	if err != nil {
		a.internalError(w, r, "criar campanha", err)
		return
	}
	a.logger.Info("campanha criada pelo painel", "campaign_id", id, "tipo", c.Type, "aneis", ringsProgress(c),
		"fontes", len(c.Sources), "programa", c.Target(), "janela", c.UseWindow, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("/painel/campanhas/%d?criada=1", id), http.StatusSeeOther)
}

// --- Detalhe da campanha ------------------------------------------------------------

type campaignJobRow struct {
	ID                                   int64
	MachineID, Short, Name, State, Label string
	Detail                               string
}

type campaignRingView struct {
	Ring    string
	Current bool
	Status  string // "ainda não liberado", "nenhuma máquina com o que fazer", ""
	Jobs    []campaignJobRow
}

type campaignView struct {
	pageBase
	C                                  campaign.Campaign
	TypeLabel, State, StateLabel, When string
	Sources, Observe, Created          string
	Rings                              []campaignRingView
	CanPause, CanResume, CanCancel     bool
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
		v.When = "na janela de cada anel"
	}
	v.Sources, v.Observe = dash(c.Target()), hoursLabel(int(c.Observe.Hours()))
	v.Created = c.CreatedAt.Format("02/01/2006 15:04") + " UTC"
	if v.Admin {
		v.CanPause = c.State == campaign.StateRunning
		v.CanResume = c.State == campaign.StatePaused
		v.CanCancel = c.State == campaign.StateRunning || c.State == campaign.StatePaused
	}
	campaignFlash(r, &v.pageBase)

	names := map[string]string{}
	if ms, err := a.store.Machines(r.Context()); err == nil {
		for _, m := range ms {
			names[m.ID] = m.Hostname
		}
	}
	for idx, ring := range c.Rings {
		rv := campaignRingView{Ring: ringLabel(ring), Current: idx == c.CurrentIdx && c.State != campaign.StateDone}
		list, err := a.store.CampaignJobs(r.Context(), c.ID, idx)
		if err != nil {
			a.internalError(w, r, "listar jobs da campanha", err)
			return
		}
		switch {
		case idx > c.LaunchedIdx:
			rv.Status = "ainda não liberado"
		case len(list) == 0:
			rv.Status = "nenhuma máquina com o que fazer"
		}
		for _, j := range list {
			rv.Jobs = append(rv.Jobs, campaignJobRow{ID: j.ID, MachineID: j.MachineID, Short: short(j.MachineID),
				Name: dash(names[j.MachineID]), State: j.State, Label: stateLabel(j.State), Detail: dash(j.Detail)})
		}
		v.Rings = append(v.Rings, rv)
	}
	a.render(w, r, http.StatusOK, "campanha", v)
}

func campaignFlash(r *http.Request, b *pageBase) {
	q := r.URL.Query()
	switch {
	case q.Has("criada"):
		b.Flash = "Campanha criada: o servidor libera o primeiro anel no próximo minuto."
	case q.Get("feito") == "pausar":
		b.Flash = "Campanha pausada: nenhum anel novo é liberado; os jobs já criados seguem."
	case q.Get("feito") == "retomar":
		b.Flash = "Campanha retomada: as falhas do anel atual ficam aceitas, e ela segue."
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
	ops := map[string]string{"pausar": campaign.OpPause, "retomar": campaign.OpResume, "cancelar": campaign.OpCancel}
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
	http.Redirect(w, r, fmt.Sprintf("%s?feito=%s&jobs=%d", back, r.PathValue("op"), n), http.StatusSeeOther)
}
