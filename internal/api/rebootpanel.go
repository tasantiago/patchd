package api

import (
	"errors"
	"fmt"
	"net/http"
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
	case err != nil || !slices.Contains(rebootDelays, delay) || (when != "agora" && when != "janela"):
		http.Redirect(w, r, back+"?erro=reinicio-form", http.StatusSeeOther)
		return
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
		"window", when == "janela", "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}
