package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// unauthorized responde 401 com o desafio Bearer. A mensagem nunca diz por que o segredo
// foi recusado (desconhecido, vencido, esgotado): isso ajudaria quem tenta adivinhar.
// O motivo vai só para o log do servidor.
func unauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="patchd"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", message)
}

// credentialProblem diz, para o log, o que há de errado no cabeçalho de um envio.
// O caso "token de enrollment no lugar da credencial" é um erro de instalação comum.
func credentialProblem(header string) string {
	cred, ok := identity.BearerToken(header)
	switch {
	case !ok:
		return "cabeçalho Authorization ausente ou fora do formato Bearer"
	case strings.HasPrefix(cred, identity.EnrollmentPrefix):
		return "token de enrollment usado no lugar da credencial da máquina"
	case !strings.HasPrefix(cred, identity.CredentialPrefix):
		return "segredo de tipo desconhecido"
	default:
		return ""
	}
}

// machineAuth exige a credencial da máquina e passa ao handler o ID da dona dela.
func (a *api) machineAuth(next func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if problem := credentialProblem(header); problem != "" {
			a.logger.Warn("envio recusado", "reason", problem, "request_id", RequestID(r.Context()), "remote", r.RemoteAddr)
			unauthorized(w, "credencial da máquina ausente ou inválida")
			return
		}
		cred, _ := identity.BearerToken(header)
		id, found, err := a.store.MachineByCredential(r.Context(), identity.Hash(cred))
		if err != nil {
			a.internalError(w, r, "autenticar máquina", err)
			return
		}
		if !found {
			a.logger.Warn("envio recusado", "reason", "credencial desconhecida (revogada ou de outro servidor)",
				"request_id", RequestID(r.Context()), "remote", r.RemoteAddr)
			unauthorized(w, "credencial da máquina ausente ou inválida")
			return
		}
		next(w, r, id)
	})
}

// enroll registra uma máquina: confere o token de enrollment, exige uma evidência forte,
// emite o ID e a credencial, e registra os alertas de identidade.
func (a *api) enroll(w http.ResponseWriter, r *http.Request) {
	tok, ok := identity.BearerToken(r.Header.Get("Authorization"))
	if !ok || !strings.HasPrefix(tok, identity.EnrollmentPrefix) {
		a.logger.Warn("enrollment sem token válido", "request_id", RequestID(r.Context()), "remote", r.RemoteAddr)
		unauthorized(w, "token de enrollment ausente ou inválido")
		return
	}
	var req protocol.EnrollRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Evidence.OSFamily == "" {
		writeError(w, http.StatusUnprocessableEntity, "missing_os", "o pedido não identifica o sistema operacional")
		return
	}
	keys, discarded := identity.Normalize(req.Evidence)
	if len(discarded) > 0 {
		a.logger.Warn("evidências descartadas no registro", "discarded", discarded,
			"hostname", req.Evidence.Hostname, "request_id", RequestID(r.Context()))
	}
	if !keys.Strong() {
		writeError(w, http.StatusUnprocessableEntity, "insufficient_evidence",
			"o pedido não traz identidade da instalação nem UUID de hardware válidos")
		return
	}

	cred, credHash := identity.NewSecret(identity.CredentialPrefix)
	m := identity.NewMachine{
		ID:             identity.NewMachineID(),
		CredentialHash: credHash,
		AgentVersion:   req.AgentVersion,
		Evidence:       req.Evidence,
		Keys:           keys,
	}
	links, err := a.store.Enroll(r.Context(), identity.Hash(tok), m)
	if errors.Is(err, identity.ErrEnrollmentRejected) {
		// O motivo (desconhecido, vencido, esgotado, revogado) fica no log; nunca o token.
		a.logger.Warn("enrollment recusado", "reason", err.Error(), "request_id", RequestID(r.Context()), "remote", r.RemoteAddr)
		unauthorized(w, "token de enrollment ausente ou inválido")
		return
	}
	if err != nil {
		a.internalError(w, r, "registrar máquina", err)
		return
	}

	a.logger.Info("máquina registrada",
		"machine_id", m.ID, "hostname", req.Evidence.Hostname, "os_family", req.Evidence.OSFamily,
		"agent_version", req.AgentVersion, "request_id", RequestID(r.Context()))
	// Os alertas vão para o log e para /api/v1/identity-links; nunca para a resposta, que
	// não pode revelar a quem pede o ID de outras máquinas.
	for _, l := range links {
		a.logger.Warn("alerta de identidade", "machine_id", l.MachineID, "related_machine_id", l.RelatedMachineID,
			"relation", l.Relation, "request_id", RequestID(r.Context()))
	}
	// A credencial só aparece nesta resposta: nenhum cache pode guardá-la.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, protocol.EnrollResponse{MachineID: m.ID, Credential: cred, ServerTime: a.now()})
}
