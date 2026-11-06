package api

import (
	"net/http"
	"slices"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
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
	known, err := a.store.CheckIn(r.Context(), id, req.AgentVersion, req.InventoryHash, r.TLS != nil)
	if err != nil {
		a.internalError(w, r, "registrar check-in", err)
		return
	}
	a.checkClock(r, id, req.SentAt)
	a.saveHostState(r, id, req.State)
	if err := a.store.SetMachineCapabilities(r.Context(), id, agentCaps(req.Capabilities)); err != nil {
		a.logger.Error("guardar as capacidades do agente", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
	}
	// Tentar assim que fechar: o programa que adiou estes jobs fechou.
	if n := len(req.Unblocked); n > 0 && n <= protocol.MaxUnblocked {
		if k, err := a.store.MarkJobsUnblocked(r.Context(), id, req.Unblocked, a.now()); err != nil {
			a.logger.Error("jobs liberados pelo agente", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
		} else if k > 0 {
			a.logger.Info("programa fechado: jobs adiados liberados", "machine_id", id, "jobs", k, "request_id", RequestID(r.Context()))
		}
	}
	// Ligar pela rede (Aula 22.2): o check-in depois do pacote é a resposta da máquina.
	if err := a.store.MarkWakeAnswered(r.Context(), id, a.now()); err != nil {
		a.logger.Error("ligar pela rede: anotar a resposta", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
	}
	policy := a.monitorPolicy()
	a.savePerf(r, id, req.Perf, policy)
	if req.Reason != "" {
		a.logger.Info("check-in antecipado", "machine_id", id, "motivo", texto.Clip(req.Reason, 40), "request_id", RequestID(r.Context()))
	}
	canal := "http"
	if r.TLS != nil {
		canal = "https"
	}
	a.count(checkinsOf, canal)
	if slices.Contains(req.Capabilities, protocol.CapabilityStandardization) {
		a.standardizeCheckin(r, id)
	}
	resp := protocol.CheckinResponse{InventoryKnown: known && req.InventoryHash != "", ServerTime: a.now(), Monitor: &policy,
		CheckinMinutes: a.checkinMinutes()}
	// A oferta vai quando a versão publicada é outra. Se ela serve, quem decide é o agente:
	// ele confere a assinatura e recusa qualquer versão que não seja mais nova que a sua.
	if v := a.offeredVersion(r); v != "" && v != req.AgentVersion {
		resp.AgentUpdate = &protocol.AgentUpdate{Version: v}
	}
	resp.OfflineCatalog = a.offlineCatalog(r)
	resp.RepoCache = a.repoAnnouncement()
	// A fonte reserva: com ela ligada, o agente que entende busca também na Microsoft quando
	// a fonte da política falha (o agente antigo ignora o campo).
	resp.WinUpdateMicrosoft = a.microsoftAllowed()
	resp.Channel = a.channelOn() // o agente novo abre a espera do canal em tempo real
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

// saveHostState grava rede e discos (Aula 12.1) e o endereço de onde veio o check-in. É
// dado de conveniência: um estado fora do formato é descartado com um aviso no log, e uma
// falha ao gravar também não derruba o check-in, que é o que mantém a máquina viva.
func (a *api) saveHostState(r *http.Request, id string, st *protocol.MachineState) {
	if st != nil {
		if err := st.Check(); err != nil {
			a.logger.Warn("estado da máquina descartado", "machine_id", id, "reason", err.Error(),
				"request_id", RequestID(r.Context()))
			st = nil
		}
	}
	if err := a.store.SaveHostState(r.Context(), id, st, remoteIP(r)); err != nil {
		a.logger.Error("gravar estado da máquina", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
	}
}

// agentCaps: as capacidades anunciadas, como vão para o banco: no máximo 16, cada uma com
// até 64 letras minúsculas, dígitos e hífens, sem repetir, em ordem (para comparar).
func agentCaps(list []string) []string {
	out := []string{}
	for _, c := range list {
		ok := c != "" && len(c) <= 64
		for _, r := range c {
			ok = ok && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
		}
		if ok && !slices.Contains(out, c) && len(out) < 16 {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}
