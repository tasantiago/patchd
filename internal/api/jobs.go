package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// JobStore é o que a API usa da fila de jobs (Aula 8.1).
type JobStore interface {
	DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error)
	JobForMachine(ctx context.Context, machineID string, id int64) (store.JobRow, bool, error)
	FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error)
	ScanReceivedAt(ctx context.Context, machineID string) (*time.Time, error)
}

// jobResult recebe o resultado de um job da máquina autenticada. O que o agente diz não
// basta (RF-08): "ok" só vira "concluido" quando o efeito aparece no servidor. Para o
// rescan, o efeito é uma busca recebida depois da entrega do job.
func (a *api) jobResult(w http.ResponseWriter, r *http.Request, machineID string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "job desconhecido")
		return
	}
	var res protocol.JobResult
	if !decodeJSON(w, r, &res) {
		return
	}
	if res.Status != protocol.JobResultOK && res.Status != protocol.JobResultFailed && res.Status != protocol.JobResultRejected {
		writeError(w, http.StatusUnprocessableEntity, "invalid_status", "status deve ser ok, falhou ou rejeitado")
		return
	}
	if len(res.Detail) > 2000 {
		res.Detail = res.Detail[:2000]
	}
	// O job tem de ser desta máquina: a credencial de uma não fecha o job de outra.
	job, found, err := a.store.JobForMachine(r.Context(), machineID, id)
	if err != nil {
		a.internalError(w, r, "ler job", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "job desconhecido")
		return
	}
	if jobs.Final(job.State) {
		// Resultado repetido (o agente não soube que o primeiro chegou) ou job já encerrado.
		writeJSON(w, http.StatusOK, protocol.SubmitResponse{Status: "unchanged", ServerTime: a.now()})
		return
	}

	state, detail := a.judge(r, job, res)
	changed, err := a.store.FinishJob(r.Context(), id, state, detail, a.now())
	if err != nil {
		a.internalError(w, r, "gravar resultado do job", err)
		return
	}
	level := a.logger.Info
	if state != jobs.StateDone {
		level = a.logger.Warn
	}
	level("job encerrado", "job_id", id, "machine_id", machineID, "type", job.Type, "state", state, "detail", detail,
		"request_id", RequestID(r.Context()))
	status := "stored"
	if !changed {
		status = "unchanged"
	}
	writeJSON(w, http.StatusOK, protocol.SubmitResponse{Status: status, ServerTime: a.now()})
}

// judge decide o estado final a partir do que o agente informou e do efeito conferido.
func (a *api) judge(r *http.Request, job store.JobRow, res protocol.JobResult) (string, string) {
	switch res.Status {
	case protocol.JobResultRejected:
		return jobs.StateRejected, "recusado pelo agente: " + res.Detail
	case protocol.JobResultFailed:
		return jobs.StateFailed, "o agente informou falha: " + res.Detail
	}
	switch job.Type {
	case jobs.TypeRescan:
		at, err := a.store.ScanReceivedAt(r.Context(), job.MachineID)
		if err != nil {
			a.logger.Error("conferir busca do job", "job_id", job.ID, "error", err)
			return jobs.StateFailed, "o servidor não conseguiu conferir a busca"
		}
		if at == nil || job.DeliveredAt == nil || at.Before(*job.DeliveredAt) {
			return jobs.StateFailed, "o agente informou sucesso, mas nenhuma busca nova chegou depois da entrega"
		}
		return jobs.StateDone, fmt.Sprintf("busca nova recebida em %s UTC", at.UTC().Format("2006-01-02 15:04:05"))
	}
	return jobs.StateFailed, "tipo de job sem conferência no servidor: " + job.Type
}
