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
	reboots []acceptedReboot
}

type acceptedReboot struct {
	id    int64
	delay time.Duration
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
		case jobs.TypeReboot:
			p, _ := jobs.ParseRebootParams(j.Params) // o Verify já conferiu
			a.Logger.Info("job aceito: reiniciar com aviso", "job_id", j.ID, "delay", p.Delay().String())
			acc.reboots = append(acc.reboots, acceptedReboot{id: j.ID, delay: p.Delay()})
		case jobs.TypeServerURL:
			p, _ := jobs.ParseServerURLParams(j.Params) // o Verify já conferiu
			reject := func(why string) {
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
			}
			switch {
			case a.SaveServerURL == nil:
				reject("este agente não troca de endereço")
			case state.PendingServerMove != nil:
				reject(fmt.Sprintf("já há uma troca de endereço pendente (job %d)", state.PendingServerMove.JobID))
			case strings.EqualFold(a.Client.BaseURL, p.URL):
				// Já usa: o resultado vai pelo endereço atual, que é o pedido.
				a.Logger.Info("job aceito: o agente já usa o endereço pedido", "job_id", j.ID, "url", p.URL)
				a.queueJobResult(state, j.ID, protocol.JobResultOK, "o agente já usa "+p.URL)
			default:
				a.Logger.Info("job aceito: trocar o endereço do servidor", "job_id", j.ID, "from", a.Client.BaseURL, "to", p.URL)
				state.PendingServerMove = &PendingServerMove{JobID: j.ID, URL: p.URL}
			}
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

// Tolerância entre o reinício marcado e a volta da máquina (desligar, POST, boot) antes de
// o agente concluir que o reinício não aconteceu.
const rebootLate = 20 * time.Minute

// scheduleReboots agenda o reinício pedido (Aula 8.6), depois das instalações do ciclo.
// Recusa sem tocar na máquina: sistema sem reinício pelo patchd, nenhum reinício pendente
// (ou não dá para saber), sem a hora do boot (o agente não conseguiria conferir), ou já
// há outro agendado. O resultado só vai depois que a máquina voltar (resolveReboot).
func (a *Agent) scheduleReboots(ctx context.Context, state *State, reboots []acceptedReboot) {
	for _, r := range reboots {
		reject := func(why string) {
			a.Logger.Warn("job recusado", "job_id", r.id, "reason", why)
			a.queueJobResult(state, r.id, protocol.JobResultRejected, why)
		}
		if a.Restart == nil || a.RebootStatus == nil {
			reject("este sistema não reinicia pelo patchd (Linux e Windows, por enquanto)")
			continue
		}
		if state.PendingReboot != nil {
			reject(fmt.Sprintf("já há um reinício agendado (job %d)", state.PendingReboot.JobID))
			continue
		}
		st, err := a.RebootStatus(ctx)
		switch {
		case err != nil:
			reject("não foi possível verificar o reinício pendente: " + err.Error())
			continue
		case st.Pending == nil:
			reject("não dá para saber se há reinício pendente nesta máquina (" + strings.TrimSpace(st.Note) + ")")
			continue
		case !*st.Pending:
			reject("nenhum reinício pendente nesta máquina; nada a fazer")
			continue
		case st.LastBoot == nil:
			reject("sem a hora do último boot, o agente não conseguiria conferir o reinício")
			continue
		}
		if err := a.Restart(ctx, r.id, r.delay); err != nil {
			a.Logger.Error("reinício não agendado", "job_id", r.id, "error", err)
			a.queueJobResult(state, r.id, protocol.JobResultFailed, "não foi possível agendar o reinício: "+err.Error())
			continue
		}
		state.PendingReboot = &PendingReboot{JobID: r.id, BootBefore: st.LastBoot.UTC(), At: a.now().UTC().Add(r.delay)}
		a.Logger.Warn("reinício agendado", "job_id", r.id, "at", state.PendingReboot.At.Format(time.RFC3339), "delay", r.delay.String())
	}
}

// resolveReboot confere, a cada ciclo, o reinício agendado: a máquina voltou (boot mais
// novo que o de antes) → a busca deste ciclo é forçada e o resultado "ok" vai depois dela;
// passou da hora com folga e não reiniciou (alguém cancelou com shutdown -c) → "falhou".
// Devolve o job a responder depois da busca (0: nenhum).
func (a *Agent) resolveReboot(ctx context.Context, state *State) int64 {
	pr := state.PendingReboot
	if pr == nil || a.RebootStatus == nil {
		return 0
	}
	st, err := a.RebootStatus(ctx)
	if err == nil && st.LastBoot != nil && st.LastBoot.After(pr.BootBefore.Add(time.Minute)) {
		a.Logger.Info("máquina reiniciada pelo job", "job_id", pr.JobID, "boot", st.LastBoot.UTC().Format(time.RFC3339))
		state.PendingReboot = nil
		return pr.JobID
	}
	if a.now().After(pr.At.Add(rebootLate)) {
		why := fmt.Sprintf("o reinício marcado para %s UTC não aconteceu (cancelado na máquina com shutdown -c?)", pr.At.Format("2006-01-02 15:04"))
		a.Logger.Error("reinício não aconteceu", "job_id", pr.JobID)
		a.queueJobResult(state, pr.JobID, protocol.JobResultFailed, why)
		state.PendingReboot = nil
	}
	return 0
}

// moveServer tenta a troca de endereço (Aula 9.2). O resultado "ok" vai pelo endereço
// novo: se ele chega, o TLS foi conferido (CA do sistema ou do -ca-file), o servidor
// aceitou a credencial lá e conferiu o canal; só então o endereço é gravado. Qualquer
// falha (certificado, rede, credencial recusada) deixa o agente onde está, e o motivo vai
// como "falhou" pelo endereço atual. Não há nova tentativa sozinha: quem corrige pede de novo.
func (a *Agent) moveServer(ctx context.Context, state *State) {
	m := state.PendingServerMove
	nc := *a.Client
	nc.BaseURL = m.URL
	a.Logger.Info("troca de endereço: entregando o resultado pelo endereço novo", "job_id", m.JobID, "to", m.URL)
	_, err := nc.SendJobResult(ctx, m.JobID, protocol.JobResult{Status: protocol.JobResultOK,
		Detail: "resultado entregue pelo endereço novo; o agente passa a usar " + m.URL, FinishedAt: a.now().UTC()})
	if ctx.Err() != nil {
		return // encerramento: a troca fica para o próximo ciclo
	}
	state.PendingServerMove = nil
	if err != nil {
		why := "o endereço novo não aceitou o resultado; o agente continua em " + a.Client.BaseURL + ": " + err.Error()
		a.Logger.Error("troca de endereço falhou", "job_id", m.JobID, "to", m.URL, "error", err)
		a.queueJobResult(state, m.JobID, protocol.JobResultFailed, why)
		return
	}
	if err := a.SaveServerURL(m.URL); err != nil {
		// O servidor já deu a troca por concluída; sem a credencial gravada, o agente segue
		// no endereço antigo, que ainda funciona, e o painel mostra o canal HTTP.
		a.Logger.Error("troca de endereço: o servidor confirmou, mas a credencial não foi gravada; o agente continua no endereço atual",
			"job_id", m.JobID, "error", err)
		return
	}
	a.Logger.Info("endereço do servidor trocado", "job_id", m.JobID, "from", a.Client.BaseURL, "to", m.URL)
	a.Client.BaseURL = m.URL
}
