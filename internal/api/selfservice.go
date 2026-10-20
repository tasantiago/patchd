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
	cat.Brand = a.brandName() // Aula 19.1b: a Central com o nome e o logotipo do painel
	cat.AppName = a.appName() // Aula 20.10
	if l := a.panelLogo(r.Context()); l != nil {
		cat.Logo = l.SHA256
	}
	writeJSON(w, http.StatusOK, cat)
}

// selfServiceLogo: GET /api/v1/agent/selfservice/logo, com a credencial da máquina (Aula 19.1b).
// O mesmo arquivo de /painel/logo; o agente confere o SHA-256 e a imagem antes de usar.
func (a *api) selfServiceLogo(w http.ResponseWriter, r *http.Request, id string) {
	l := a.panelLogo(r.Context())
	if l == nil {
		writeError(w, http.StatusNotFound, "no_logo", "o painel não tem logotipo")
		return
	}
	h := w.Header()
	h.Set("Content-Type", l.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", fmt.Sprint(len(l.Data)))
	_, _ = w.Write(l.Data)
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
	case protocol.SelfServiceApproval: // Aula 18.3: guardado para a TI
		a.logger.Info("autoatendimento: pedido aguardando a TI", "machine_id", id, "pedido", o.RequestID, "app", req.App, "acao", req.Action,
			"request_id", RequestID(r.Context()))
		a.auditAs(r, req.Account, "autoatendimento", "autoatendimento.solicitacao", id,
			fmt.Sprintf("pedido %d · %s %s · %s · visto conectado: %s", o.RequestID, req.App, o.Version, req.Action, seen))
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
