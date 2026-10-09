package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// acceptedJobs são os jobs aceitos num check-in: os rescans e os updates. Os dois forçam a
// busca do ciclo, e os resultados só vão depois que ela for entregue.
type acceptedJobs struct {
	rescans []int64
	updates []acceptedUpdate
}

type acceptedUpdate struct {
	id   int64
	pkgs []jobs.Package
}

func (j acceptedJobs) forceScan() bool { return len(j.rescans) > 0 || len(j.updates) > 0 }

// acceptJobs confere os jobs recebidos no check-in (Aula 8.1). Os recusados viram resultado
// "rejeitado", com o motivo.
func (a *Agent) acceptJobs(received []protocol.SignedJob, state *State) acceptedJobs {
	var acc acceptedJobs
	for _, env := range received {
		j, err := jobs.Verify(a.JobKey, env, a.MachineID, a.now(), state.LastJobID)
		if err != nil {
			id := jobs.ID(env)
			a.Logger.Warn("job recusado", "job_id", id, "reason", err.Error())
			if id > 0 {
				a.queueJobResult(state, id, protocol.JobResultRejected, err.Error())
			}
			continue
		}
		state.LastJobID = j.ID
		switch j.Type {
		case jobs.TypeRescan:
			a.Logger.Info("job aceito: busca de atualizações agora", "job_id", j.ID)
			acc.rescans = append(acc.rescans, j.ID)
		case jobs.TypeUpdate:
			p, _ := jobs.ParseUpdateParams(j.Params) // o Verify já conferiu
			if a.Install == nil {
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", "este sistema não instala pacotes pelo patchd")
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, "este sistema não instala pacotes pelo patchd (só Linux, por enquanto)")
				continue
			}
			a.Logger.Info("job aceito: atualizar pacotes", "job_id", j.ID, "packages", strings.Join(p.Names(), ","))
			acc.updates = append(acc.updates, acceptedUpdate{id: j.ID, pkgs: p.Packages})
		}
	}
	return acc
}

// runUpdates instala os pacotes dos jobs update, um job por vez, e devolve os resultados,
// que vão para a fila depois da busca (o servidor confere o efeito por ela).
func (a *Agent) runUpdates(ctx context.Context, updates []acceptedUpdate) []PendingJobResult {
	var out []PendingJobResult
	for _, u := range updates {
		a.Logger.Info("instalação iniciada", "job_id", u.id, "packages", len(u.pkgs))
		start := a.now()
		detail, err := a.Install(ctx, u.id, u.pkgs)
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrNotPending):
			status, detail = protocol.JobResultRejected, err.Error()
			a.Logger.Warn("job recusado", "job_id", u.id, "reason", detail)
		case err != nil:
			status = protocol.JobResultFailed
			detail = strings.TrimSpace(err.Error() + " " + detail)
			a.Logger.Error("instalação falhou", "job_id", u.id, "error", detail)
		default:
			a.Logger.Info("instalação concluída", "job_id", u.id, "detail", detail,
				"duration", a.now().Sub(start).Round(time.Second).String())
		}
		out = append(out, PendingJobResult{ID: u.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out
}

func (a *Agent) queueJobResult(state *State, id int64, status, detail string) {
	state.PendingJobResults = append(state.PendingJobResults, PendingJobResult{ID: id,
		Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
}

// flushJobResults envia os resultados guardados, na ordem. O servidor fora interrompe o
// envio (os que faltam ficam para o próximo ciclo); uma recusa do servidor descarta o
// resultado, que nunca vai passar.
func (a *Agent) flushJobResults(ctx context.Context, state *State) outcome {
	for len(state.PendingJobResults) > 0 {
		p := state.PendingJobResults[0]
		r, err := a.Client.SendJobResult(ctx, p.ID, p.Result)
		out := a.classify(err, fmt.Sprintf("resultado do job %d", p.ID))
		if out == outcomeTransient || out == outcomeUnauthorized {
			return out
		}
		if out == outcomeOK {
			a.Logger.Info("resultado do job enviado", "job_id", p.ID, "status", p.Result.Status, "server", r.Status)
		}
		state.PendingJobResults = state.PendingJobResults[1:]
	}
	return outcomeOK
}
