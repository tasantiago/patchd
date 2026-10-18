package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
)

// Autoatendimento pelo agente (Aula 17.2). O agente se autentica com a credencial da máquina
// e diz quem pediu: a conta e os grupos do token do Windows de quem chamou o app (17.3). O
// servidor monta a loja desse usuário nessa máquina e, no pedido, refaz a loja e cria o job
// thirdparty assinado, com o usuário como autor e na auditoria.

func (a *api) selfService() *selfservice.Service {
	return &selfservice.Service{Store: a.store, Manifest: a.tpManifest, Key: a.jobKey, PackagesDir: a.packagesDir(), Now: a.now}
}

func (a *api) selfServiceCatalog(w http.ResponseWriter, r *http.Request, id string) {
	var u protocol.SelfServiceUser
	if !decodeJSON(w, r, &u) {
		return
	}
	if err := u.Check(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	cat, err := a.selfService().Catalog(r.Context(), id, u)
	if errors.Is(err, selfservice.ErrOff) {
		writeError(w, http.StatusServiceUnavailable, "selfservice_off", "o autoatendimento está desligado neste servidor")
		return
	}
	if err != nil {
		a.internalError(w, r, "montar a loja", err)
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

func (a *api) selfServiceRequest(w http.ResponseWriter, r *http.Request, id string) {
	var req protocol.SelfServiceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := req.Check(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	if len(req.App) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_app", "programa inválido")
		return
	}
	o, err := a.selfService().Request(r.Context(), id, req)
	if errors.Is(err, selfservice.ErrOff) {
		writeError(w, http.StatusServiceUnavailable, "selfservice_off", "o autoatendimento está desligado neste servidor")
		return
	}
	if err != nil {
		a.internalError(w, r, "atender o pedido", err)
		return
	}
	seen := "não"
	if o.Seen {
		seen = "sim"
	}
	switch o.Response.Status {
	case protocol.SelfServiceCreated:
		a.logger.Info("autoatendimento: pedido atendido", "machine_id", id, "job_id", o.Job.ID, "app", req.App, "version", o.Version,
			"acao", req.Action, "visto_conectado", o.Seen, "request_id", RequestID(r.Context()))
		a.auditAs(r, req.Account, "autoatendimento", "autoatendimento.pedido", id,
			fmt.Sprintf("job %d · %s %s · %s · visto conectado: %s", o.Job.ID, selfServiceWhat(req), o.Version, req.Action, seen))
	default:
		a.logger.Warn("autoatendimento: pedido não atendido", "machine_id", id, "app", req.App, "acao", req.Action, "status", o.Response.Status,
			"motivo", o.Why, "request_id", RequestID(r.Context()))
		a.auditAs(r, req.Account, "autoatendimento", "autoatendimento.recusado", id,
			fmt.Sprintf("%s · %s · %s: %s", selfServiceWhat(req), req.Action, o.Response.Status, o.Why))
	}
	writeJSON(w, http.StatusOK, o.Response)
}

// selfServiceWhat é o objeto do pedido na auditoria: o programa, ou o computador no
// reinício (Aula 17.6).
func selfServiceWhat(req protocol.SelfServiceRequest) string {
	if req.App == "" {
		return "computador"
	}
	return req.App
}
