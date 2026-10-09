package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Jobs no painel (Aula 8.2): a lista de jobs, os jobs de cada máquina e, para o perfil
// admin, "Buscar atualizações agora" (um job rescan) e cancelar um job ainda pendente.
// Criar exige a chave de jobs no processo do servidor (WithJobKey); sem ela, o painel só
// mostra, e os jobs são criados pela linha de comando.

const (
	panelRescanTTL   = 3 * time.Hour // uma busca pedida agora não faz sentido amanhã
	machineJobsShown = 10
	fleetJobsShown   = 50
	openJobsScan     = 50 // quantos jobs recentes da máquina são olhados atrás de um em aberto
)

// WithJobKey liga a criação de jobs pelo painel, com a chave privada de jobs.
func WithJobKey(key ed25519.PrivateKey) Option {
	return func(a *api) {
		if len(key) == ed25519.PrivateKeySize {
			a.jobKey = key
		}
	}
}

func stateLabel(s string) string {
	switch s {
	case jobs.StateDone:
		return "concluído"
	case jobs.StateRejected:
		return "recusado"
	}
	return s
}

func typeLabel(t string) string {
	if t == jobs.TypeRescan {
		return "buscar atualizações"
	}
	return t
}

type jobRowView struct {
	ID                                              int64
	MachineID, Short, Name, Type, State, StateLabel string
	Created, By, Delivered, Finished, Detail        string
	CanCancel                                       bool
}

func (a *api) jobRows(list []store.JobRow, names map[string]string, admin bool) []jobRowView {
	now := a.now()
	at := func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return ago(now.Sub(*t))
	}
	rows := make([]jobRowView, 0, len(list))
	for _, j := range list {
		created := j.CreatedAt
		name := "—"
		if n, ok := names[j.MachineID]; ok {
			name = n
		}
		rows = append(rows, jobRowView{ID: j.ID, MachineID: j.MachineID, Short: short(j.MachineID), Name: name,
			Type: typeLabel(j.Type), State: j.State, StateLabel: stateLabel(j.State), Created: at(&created), By: j.CreatedBy,
			Delivered: at(j.DeliveredAt), Finished: at(j.FinishedAt), Detail: dash(j.Detail),
			CanCancel: admin && j.State == jobs.StatePending})
	}
	return rows
}

// expireJobs fecha os jobs vencidos antes de listar, como o "job list" da linha de
// comando. Uma falha não impede a lista: ela só mostra o estado anterior.
func (a *api) expireJobs(r *http.Request) {
	if _, err := a.store.ExpireJobs(r.Context(), a.now()); err != nil {
		a.logger.Error("expirar jobs", "error", err, "request_id", RequestID(r.Context()))
	}
}

// machineJobs são os jobs mostrados no detalhe da máquina.
func (a *api) machineJobs(r *http.Request, id string, admin bool) ([]jobRowView, error) {
	a.expireJobs(r)
	list, err := a.store.Jobs(r.Context(), id, machineJobsShown)
	if err != nil {
		return nil, err
	}
	return a.jobRows(list, nil, admin), nil
}

// jobFlash traduz o resultado de uma ação (na URL do redirecionamento) numa mensagem fixa.
func jobFlash(q map[string][]string, b *pageBase) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	n, err := strconv.ParseInt(get("job"), 10, 64)
	if err != nil || n <= 0 {
		n = 0
	}
	switch {
	case get("erro") == "job-aberto" && n > 0:
		b.Error = fmt.Sprintf("Já existe uma busca pedida e ainda em aberto para esta máquina (job %d). Espere o resultado ou cancele o job.", n)
	case get("erro") == "sem-chave":
		b.Error = "O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE); nada foi criado."
	case get("erro") == "cancelar" && n > 0:
		b.Error = fmt.Sprintf("O job %d não está mais pendente; nada mudou. Um job entregue não se cancela: o agente pode estar executando.", n)
	case get("criado") != "" && n > 0:
		b.Flash = fmt.Sprintf("Job %d criado: a máquina recebe no próximo check-in (até cerca de 1 h; reiniciar o agente antecipa).", n)
	case get("cancelado") != "" && n > 0:
		b.Flash = fmt.Sprintf("Job %d cancelado.", n)
	}
}

// jobTable é a tabela de jobs, na lista da frota (sem MachineID: com as colunas da
// máquina) e no detalhe de uma máquina.
type jobTable struct {
	Rows      []jobRowView
	Admin     bool
	MachineID string
}

type jobsView struct {
	pageBase
	Jobs jobTable
	N    int
}

// jobsPage lista os jobs mais recentes da frota.
func (a *api) jobsPage(w http.ResponseWriter, r *http.Request) {
	v := jobsView{pageBase: a.base(r, "Jobs", "jobs"), N: fleetJobsShown}
	jobFlash(r.URL.Query(), &v.pageBase)
	a.expireJobs(r)
	list, err := a.store.Jobs(r.Context(), "", fleetJobsShown)
	if err != nil {
		a.internalError(w, r, "listar jobs", err)
		return
	}
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	names := make(map[string]string, len(machines))
	for _, m := range machines {
		names[m.ID] = dash(m.Hostname)
	}
	v.Jobs = jobTable{Rows: a.jobRows(list, names, v.Admin), Admin: v.Admin}
	a.render(w, r, http.StatusOK, "jobs", v)
}

// openJob procura um job em aberto do tipo na máquina: um segundo clique não cria outro.
func (a *api) openJob(ctx context.Context, machineID, typ string) (int64, error) {
	list, err := a.store.Jobs(ctx, machineID, openJobsScan)
	if err != nil {
		return 0, err
	}
	for _, j := range list {
		if j.Type == typ && !jobs.Final(j.State) && j.NotAfter.After(a.now()) {
			return j.ID, nil
		}
	}
	return 0, nil
}

// rescanAction cria um job rescan para a máquina, com o usuário do painel como autor.
func (a *api) rescanAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := machinePath(id)
	if a.jobKey == nil {
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeRescan)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=job-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey,
		jobs.Request{MachineID: id, Type: jobs.TypeRescan, TTL: panelRescanTTL, By: s.Username}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		// Aposentada (ou inexistente): o detalhe mostra "não encontrada".
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "username", s.Username,
		"request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}

// cancelJobAction cancela um job pendente. O formulário diz para onde voltar (a máquina
// ou a lista de jobs); o valor é conferido como qualquer ID de máquina.
func (a *api) cancelJobAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	back := "/painel/jobs"
	if m := r.PostFormValue("maquina"); m != "" && machineIDPattern.MatchString(m) {
		back = machinePath(m)
	}
	ok, err := a.store.CancelJob(r.Context(), id, s.Username)
	if err != nil {
		a.internalError(w, r, "cancelar job", err)
		return
	}
	if !ok {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=cancelar&job=%d", back, id), http.StatusSeeOther)
		return
	}
	a.logger.Info("job cancelado pelo painel", "job_id", id, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?cancelado=1&job=%d", back, id), http.StatusSeeOther)
}
