package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Atualização pelo painel (Aula 8.4): o admin marca as pendências do Linux e pede
// "atualizar agora" ou "na próxima janela do anel". O formulário só diz QUAIS pendências;
// os pacotes e as versões vêm da avaliação que o servidor faz na hora, nunca do navegador.

const panelUpdateTTL = 6 * time.Hour // "agora": o agente tem algumas horas para fazer o check-in

// updateAction cria o job update com os pacotes binários das pendências marcadas.
func (a *api) updateAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := machinePath(id)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	chosen := r.PostForm["item"]
	when := r.PostFormValue("quando")
	switch {
	case a.jobKey == nil:
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	case a.compliance == nil:
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	case len(chosen) == 0:
		http.Redirect(w, r, back+"?erro=nada-marcado", http.StatusSeeOther)
		return
	case when != "agora" && when != "janela":
		http.Redirect(w, r, back+"?erro=quando", http.StatusSeeOther)
		return
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeUpdate)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=update-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	d, found, err := a.compliance.Detail(ctx, id)
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	params := jobs.ParamsFromPending(d.Items, chosen)
	if len(params.Packages) == 0 {
		// As marcadas já não estão pendentes (a avaliação mudou) ou não são do Linux.
		http.Redirect(w, r, back+"?erro=nada-marcado", http.StatusSeeOther)
		return
	}
	if err := params.Validate(); err != nil {
		a.logger.Warn("update pelo painel recusado", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
		http.Redirect(w, r, back+"?erro=pacotes", http.StatusSeeOther)
		return
	}
	raw, _ := json.Marshal(params)
	req := jobs.Request{MachineID: id, Type: jobs.TypeUpdate, TTL: panelUpdateTTL, By: s.Username, Params: raw}
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
		now := a.now()
		req.NotBefore, req.TTL = start, end.Sub(later(now, start))
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
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "packages", len(params.Packages),
		"window", when == "janela", "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}

// nextWindow devolve a próxima janela do anel da máquina (ok false: o anel não tem janela).
func (a *api) nextWindow(r *http.Request, id string) (start, end time.Time, ok bool, err error) {
	ring, found, err := a.store.MachineRing(r.Context(), id)
	if err != nil || !found {
		return start, end, false, err
	}
	windows, err := a.store.Windows(r.Context())
	if err != nil {
		return start, end, false, err
	}
	rw, ok := store.WindowFor(windows, ring)
	if !ok {
		return start, end, false, nil
	}
	start, end = rw.Window.Next(a.now(), jobs.MinTTL)
	return start, end, true, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
