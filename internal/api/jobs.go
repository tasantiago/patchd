package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/version"
)

// JobStore é o que a API usa da fila de jobs: entrega e resultado (Aula 8.1), criação,
// lista e cancelamento pelo painel (Aula 8.2).
type JobStore interface {
	DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error)
	JobForMachine(ctx context.Context, machineID string, id int64) (store.JobRow, bool, error)
	FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error)
	ScanReceivedAt(ctx context.Context, machineID string) (*time.Time, error)
	jobs.Queue // NextJobID e CreateJob
	ExpireJobs(ctx context.Context, now time.Time) (int64, error)
	Jobs(ctx context.Context, machineID string, limit int) ([]store.JobRow, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
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
	// Os dois tipos exigem uma busca recebida depois da entrega: é nela que o efeito aparece.
	at, err := a.store.ScanReceivedAt(r.Context(), job.MachineID)
	if err != nil {
		a.logger.Error("conferir busca do job", "job_id", job.ID, "error", err)
		return jobs.StateFailed, "o servidor não conseguiu conferir a busca"
	}
	if at == nil || job.DeliveredAt == nil || at.Before(*job.DeliveredAt) {
		return jobs.StateFailed, "o agente informou sucesso, mas nenhuma busca nova chegou depois da entrega"
	}
	when := at.UTC().Format("2006-01-02 15:04:05")
	switch job.Type {
	case jobs.TypeRescan:
		return jobs.StateDone, fmt.Sprintf("busca nova recebida em %s UTC", when)
	case jobs.TypeUpdate:
		return a.judgeUpdate(r, job, when)
	}
	return jobs.StateFailed, "tipo de job sem conferência no servidor: " + job.Type
}

// judgeUpdate confere, na busca nova, que nenhum pacote pedido continua abaixo da versão
// pedida (Aula 8.4). No apt, o item pendente traz a versão instalada; no dnf, um advisory
// pendente com correção até a versão pedida mostra que ela não foi instalada.
func (a *api) judgeUpdate(r *http.Request, job store.JobRow, when string) (string, string) {
	var payload jobs.Job
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return jobs.StateFailed, "o servidor não conseguiu ler os pacotes do job"
	}
	params, err := jobs.ParseUpdateParams(payload.Params)
	if err != nil {
		return jobs.StateFailed, "pacotes do job ilegíveis: " + err.Error()
	}
	rep, found, err := a.store.LatestScan(r.Context(), job.MachineID)
	if err != nil || !found {
		return jobs.StateFailed, "o servidor não conseguiu ler a busca nova"
	}
	still := stillPending(params.Packages, rep)
	if len(still) > 0 {
		return jobs.StateFailed, fmt.Sprintf("a busca de %s UTC ainda mostra pendente: %s", when, strings.Join(still, "; "))
	}
	return jobs.StateDone, fmt.Sprintf("%d pacote(s) conferidos na busca de %s UTC: %s", len(params.Packages), when, strings.Join(params.Names(), ", "))
}

// stillPending lista os pacotes pedidos que a busca ainda mostra abaixo da versão pedida.
// Uma fonte que falhou na busca não prova nada: o pacote conta como pendente.
func stillPending(pkgs []jobs.Package, rep protocol.PatchScanReport) []string {
	var out []string
	for _, p := range pkgs {
		problem := ""
		for _, s := range rep.Scans {
			if s.Source != "apt" && s.Source != "dnf" {
				continue
			}
			if s.Missing == nil {
				problem = "a busca do " + s.Source + " falhou (" + s.Error + ")"
				break
			}
			for _, u := range s.Missing {
				if u.Package != p.Name {
					continue
				}
				if s.Source == "apt" {
					if c, err := version.CompareDpkg(u.InstalledVersion, p.MinVersion); u.InstalledVersion == "" || err != nil || c < 0 {
						problem = fmt.Sprintf("%s instalada %s, pedida ≥ %s", p.Name, dashIfEmpty(u.InstalledVersion), p.MinVersion)
					}
				} else if c, err := version.CompareRPM(u.FixedVersion, p.MinVersion); err != nil || c <= 0 {
					problem = fmt.Sprintf("%s com %s pendente (correção %s, pedida ≥ %s)", p.Name, u.ID, u.FixedVersion, p.MinVersion)
				}
			}
		}
		if problem != "" {
			out = append(out, problem)
		}
	}
	return out
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
