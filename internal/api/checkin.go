package api

import (
	"net/http"
	"slices"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// checkin é o contato periódico da máquina (Aula 4.4): atualiza o último contato e diz se
// o servidor já tem o inventário com o hash que o agente acabou de coletar. Na maioria das
// horas, o inventário não muda, e o agente economiza o envio inteiro.
func (a *api) checkin(w http.ResponseWriter, r *http.Request, id string) {
	var req protocol.CheckinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.InventoryHash != "" && !hashPattern.MatchString(req.InventoryHash) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_hash", "inventory_hash fora do formato sha256:<64 hex>")
		return
	}
	known, err := a.store.CheckIn(r.Context(), id, req.AgentVersion, req.InventoryHash)
	if err != nil {
		a.internalError(w, r, "registrar check-in", err)
		return
	}
	a.checkClock(r, id, req.SentAt)
	resp := protocol.CheckinResponse{InventoryKnown: known && req.InventoryHash != "", ServerTime: a.now()}
	// A oferta vai quando a versão publicada é outra. Se ela serve, quem decide é o agente:
	// ele confere a assinatura e recusa qualquer versão que não seja mais nova que a sua.
	if v := a.offeredVersion(r); v != "" && v != req.AgentVersion {
		resp.AgentUpdate = &protocol.AgentUpdate{Version: v}
	}
	resp.OfflineCatalog = a.offlineCatalog(r)
	resp.RepoCache = a.repoCache
	if slices.Contains(req.Capabilities, protocol.CapabilityJobs) {
		// Uma falha aqui não derruba o check-in: os jobs ficam pendentes para o próximo.
		delivered, err := a.store.DeliverJobs(r.Context(), id, a.now())
		if err != nil {
			a.logger.Error("entrega de jobs", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
		}
		for _, j := range delivered {
			a.logger.Info("job entregue", "machine_id", id, "job_id", jobs.ID(j), "request_id", RequestID(r.Context()))
		}
		resp.Jobs = delivered
	}
	writeJSON(w, http.StatusOK, resp)
}
