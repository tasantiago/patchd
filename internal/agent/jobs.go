package agent

import (
	"context"
	"fmt"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// acceptJobs confere os jobs recebidos no check-in (Aula 8.1). Os recusados viram resultado
// "rejeitado", com o motivo. Devolve os IDs aceitos de rescan: eles forçam a busca deste
// ciclo e são respondidos depois que ela for entregue.
func (a *Agent) acceptJobs(received []protocol.SignedJob, state *State) []int64 {
	var rescans []int64
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
			rescans = append(rescans, j.ID)
		}
	}
	return rescans
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
