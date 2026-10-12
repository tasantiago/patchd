package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
)

// Troca para HTTPS pelo painel (Aula 9.2): um job server_url com a URL pública do servidor,
// para a máquina cujo último check-in chegou sem TLS.

const panelServerURLTTL = 24 * time.Hour // a máquina pode estar desligada até amanhã

// WithPublicURL liga o botão "Passar para HTTPS": a URL que os agentes devem usar. A
// configuração do servidor já conferiu que é https.
func WithPublicURL(u string) Option {
	return func(a *api) {
		if n, err := jobs.NormalizeServerURL(u); err == nil {
			a.publicURL = n
		}
	}
}

// channelView preenche o canal do último check-in e decide se o botão aparece.
func (a *api) channelView(r *http.Request, id string, v *machineView) error {
	tls, err := a.store.CheckinTLS(r.Context(), id)
	if err != nil {
		return err
	}
	switch {
	case tls == nil:
		v.Channel = "— (nenhum check-in desde a Aula 9.2)"
	case *tls:
		v.Channel = "HTTPS"
	default:
		v.Channel = "HTTP (porta de transição)"
	}
	v.PublicURL = a.publicURL
	v.CanHTTPS = v.Admin && v.JobsOn && a.publicURL != "" && (tls == nil || !*tls)
	return nil
}

// serverURLAction cria o job server_url com a URL pública. Um segundo clique, com o
// primeiro ainda em aberto, não cria outro.
func (a *api) serverURLAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := machinePath(id)
	switch {
	case a.jobKey == nil:
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	case a.publicURL == "":
		http.Redirect(w, r, back+"?erro=sem-url", http.StatusSeeOther)
		return
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeServerURL)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=https-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}
	params, _ := jobs.ServerURLParamsJSON(a.publicURL)
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey,
		jobs.Request{MachineID: id, Type: jobs.TypeServerURL, TTL: panelServerURLTTL, By: s.Username, Params: params}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "url", a.publicURL,
		"username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}
