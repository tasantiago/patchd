package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
)

// Reinício pelo painel (Aula 8.6; Windows na 8.7): o admin pede "reiniciar com aviso" numa
// máquina Linux ou Windows com reinício pendente na última busca. O aviso é escolhido numa lista fixa; o agente
// confere de novo, na hora, se o reinício ainda é necessário.

const panelRebootTTL = 3 * time.Hour

var rebootDelays = []int{5, 15, 30, 60} // minutos oferecidos no painel

// Reinício em horário marcado: por quanto tempo o pedido vale a partir do horário (a máquina
// precisa fazer check-in nesse intervalo), até quantos dias à frente, e quanto antes o pacote
// de ligar sai, quando pedido.
var (
	rebootValidities = []int{60, 120, 180, 240, 360}
	rebootAhead      = 14 * 24 * time.Hour
	wakeBefore       = 10 * time.Minute
)

// scheduleInput: o datetime-local do formulário (2026-11-06T02:00).
const scheduleInput = "2006-01-02T15:04"

// nextNight: a sugestão do formulário, a próxima 02:00 no fuso do painel.
func nextNight(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), 2, 0, 0, 0, loc)
	if !t.After(n.Add(time.Hour)) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (a *api) panelLocation() *time.Location {
	if loc, err := loadZone(a.tz()); err == nil {
		return loc
	}
	return time.UTC
}

// canReboot diz se a máquina é Linux ou Windows e a última busca mostrou reinício pendente.
func (a *api) canReboot(r *http.Request, id string) (bool, error) {
	inv, found, err := a.store.LatestInventory(r.Context(), id)
	if err != nil || !found || (inv.OS.Family != "linux" && inv.OS.Family != "windows") {
		return false, err
	}
	rep, found, err := a.store.LatestScan(r.Context(), id)
	if err != nil || !found || rep.Reboot == nil || rep.Reboot.Pending == nil {
		return false, err
	}
	return *rep.Reboot.Pending, nil
}

func (a *api) rebootAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := machinePath(id)
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	delay, err := strconv.Atoi(r.PostFormValue("aviso"))
	when := r.PostFormValue("quando")
	switch {
	case a.jobKey == nil:
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	case err != nil || !slices.Contains(rebootDelays, delay) || (when != "agora" && when != "janela" && when != "horario"):
		http.Redirect(w, r, back+"?erro=reinicio-form", http.StatusSeeOther)
		return
	}
	// Em horário marcado: o início no fuso do painel, no futuro e até 14 dias à frente.
	var at time.Time
	var valid int
	if when == "horario" {
		loc := a.panelLocation()
		at, err = time.ParseInLocation(scheduleInput, r.PostFormValue("inicio"), loc)
		valid, _ = strconv.Atoi(r.PostFormValue("validade"))
		now := a.now()
		if err != nil || !slices.Contains(rebootValidities, valid) || !at.After(now.Add(2*time.Minute)) || at.After(now.Add(rebootAhead)) {
			http.Redirect(w, r, back+"?erro=reinicio-horario", http.StatusSeeOther)
			return
		}
	}
	ok, err := a.canReboot(r, id)
	if err != nil {
		a.internalError(w, r, "conferir reinício pendente", err)
		return
	}
	if !ok {
		http.Redirect(w, r, back+"?erro=sem-reinicio", http.StatusSeeOther)
		return
	}
	// Aula 15.12: reiniciar agora com gente trabalhando na máquina pede confirmação. O aviso
	// do agente avisa, mas quem decide derrubar o trabalho de alguém é o admin, sabendo disso.
	// Na janela do grupo de implantação, não: a janela já é o horário combinado para isso.
	active := 0
	if when == "agora" {
		h, found, err := a.store.HostState(r.Context(), id)
		if err != nil {
			a.internalError(w, r, "ler quem está conectado", err)
			return
		}
		if found && h.State != nil {
			active = activeSessions(h.State.Users)
		}
		if active > 0 && r.PostFormValue("ciente") != "1" {
			http.Redirect(w, r, fmt.Sprintf("%s?erro=reinicio-usuarios&n=%d", back, active), http.StatusSeeOther)
			return
		}
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeReboot)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=reinicio-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}
	params, err := jobs.RebootParamsJSON(time.Duration(delay) * time.Minute)
	if err != nil {
		a.internalError(w, r, "montar o reinício", err)
		return
	}
	req := jobs.Request{MachineID: id, Type: jobs.TypeReboot, TTL: panelRebootTTL, By: s.Username, Params: params}
	if when == "horario" {
		req.NotBefore, req.TTL = at.UTC(), time.Duration(valid)*time.Minute
	}
	if when == "janela" {
		start, end, ok, err := a.nextWindow(r, id)
		if err != nil {
			a.internalError(w, r, "ler janela", err)
			return
		}
		if !ok {
			http.Redirect(w, r, back+"?erro=sem-janela", http.StatusSeeOther)
			return
		}
		req.NotBefore, req.TTL = start, end.Sub(later(a.now(), start))
	}
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey, req, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "delay_min", delay,
		"window", when == "janela", "sessoes_ativas", active, "username", s.Username, "request_id", RequestID(r.Context()))
	if when != "horario" {
		a.audit(r, s, "job.criar", id, fmt.Sprintf("job %d · %s · aviso %d min · sessões ativas %d", rec.ID, typeLabel(rec.Type), delay, active))
		http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
		return
	}
	a.audit(r, s, "job.criar", id, fmt.Sprintf("job %d · %s · aviso %d min · a partir de %s por %d min", rec.ID, typeLabel(rec.Type), delay,
		at.Format("02/01/2006 15:04 MST"), valid))
	q := url.Values{"criado": {"1"}, "job": {strconv.FormatInt(rec.ID, 10)}, "reinicio": {"agendado"}, "em": {strconv.FormatInt(at.Unix(), 10)}}
	// Ligar pela rede antes: o pacote sai wakeBefore antes do horário, pelos repetidores da
	// sub-rede. Não dar para ligar não desfaz o reinício: só avisa.
	if r.PostFormValue("ligar") == "1" {
		res, err := a.requestWake(r, s, id, at.Add(-wakeBefore))
		switch {
		case err != nil && !errors.Is(err, errWakeNotFound):
			a.internalError(w, r, "agendar o ligar pela rede", err)
			return
		case err != nil:
			q.Set("wol", "ligar-erro=rede")
		case res.problem != "":
			q.Set("wol", res.problem)
		default:
			q.Set("wol", "ok")
			q.Set("wn", strconv.Itoa(res.jobs))
		}
	}
	http.Redirect(w, r, back+"?"+q.Encode(), http.StatusSeeOther)
}
