package agent

import (
	"context"
	"crypto/ed25519"
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
	rescans    []int64
	updates    []acceptedUpdate
	reboots    []acceptedReboot
	thirdParty []acceptedThirdParty
	winUpdates []acceptedWinUpdate
	bios       []acceptedBIOS
	scripts    []acceptedScript
	// docs/bios-config.md
	biosSettings []acceptedBIOSSettings
}

type acceptedBIOSSettings struct {
	id int64
	p  jobs.BIOSSettingsParams
}

type acceptedScript struct {
	id int64
	p  jobs.ScriptParams
}

type acceptedBIOS struct {
	id int64
	p  jobs.BIOSPasswordParams
}

type acceptedWinUpdate struct {
	id int64
	p  jobs.WinUpdateParams
}

type acceptedThirdParty struct {
	id int64
	p  jobs.ThirdPartyParams
}

type acceptedReboot struct {
	id    int64
	delay time.Duration
}

type acceptedUpdate struct {
	id   int64
	pkgs []jobs.Package
}

func (j acceptedJobs) forceScan() bool {
	return len(j.rescans) > 0 || len(j.updates) > 0 || len(j.winUpdates) > 0
}

// acceptJobs confere os jobs recebidos no check-in (Aula 8.1). Os recusados viram resultado
// "rejeitado", com o motivo.
func (a *Agent) acceptJobs(received []protocol.SignedJob, state *State) acceptedJobs {
	var acc acceptedJobs
	for _, env := range received {
		keys := []ed25519.PublicKey{a.JobKey}
		if a.JobKeyNext != nil {
			keys = append(keys, a.JobKeyNext)
		}
		j, err := jobs.VerifyAny(keys, env, a.MachineID, a.now(), state.LastJobID)
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
			// O Verify já conferiu. Com a hora escolhida pelo usuário (Aula 17.6), espera até ela.
			p, _ := jobs.ParseRebootParams(j.Params)
			delay := p.DelayAt(a.now())
			a.Logger.Info("job aceito: reiniciar com aviso", "job_id", j.ID, "delay", delay.String())
			acc.reboots = append(acc.reboots, acceptedReboot{id: j.ID, delay: delay})
		case jobs.TypeThirdParty:
			p, _ := jobs.ParseThirdPartyParams(j.Params) // o Verify já conferiu
			if a.ThirdParty == nil {
				why := "este sistema não instala programas de terceiros pelo patchd (só Windows)"
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
				continue
			}
			a.Logger.Info("job aceito: instalar programa de terceiros", "job_id", j.ID, "app", p.App, "version", p.Version,
				"installer", p.Installer)
			acc.thirdParty = append(acc.thirdParty, acceptedThirdParty{id: j.ID, p: p})
		case jobs.TypeScript:
			p, _ := jobs.ParseScriptParams(j.Params) // o Verify já conferiu
			if a.Script == nil {
				why := "este agente não executa bundles de script"
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
				continue
			}
			a.Logger.Info("job aceito: executar bundle de script", "job_id", j.ID, "bundle", p.Bundle, "entry", p.Entry,
				"runner", p.Runner)
			acc.scripts = append(acc.scripts, acceptedScript{id: j.ID, p: p})
		case jobs.TypeWinUpdate:
			p, _ := jobs.ParseWinUpdateParams(j.Params) // o Verify já conferiu
			if a.WinUpdate == nil {
				why := "este sistema não instala atualizações do Windows (só o agente do Windows)"
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
				continue
			}
			a.Logger.Info("job aceito: instalar atualizações do Windows", "job_id", j.ID, "updates", strings.Join(p.Labels(), ","))
			acc.winUpdates = append(acc.winUpdates, acceptedWinUpdate{id: j.ID, p: p})
		case jobs.TypeBIOSPassword:
			p, _ := jobs.ParseBIOSPasswordParams(j.Params) // o Verify já conferiu
			if a.BIOSPassword == nil {
				why := "este agente não grava a senha da BIOS (só o agente do Windows, com a caixa de cifragem)"
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
				continue
			}
			a.Logger.Info("job aceito: gravar a senha de administrador da BIOS", "job_id", j.ID, "guarda", p.Guard, "fabricante", p.Vendor)
			acc.bios = append(acc.bios, acceptedBIOS{id: j.ID, p: p})
		case jobs.TypeBIOSSettings:
			p, _ := jobs.ParseBIOSSettingsParams(j.Params) // o Verify já conferiu
			if a.BIOSSettings == nil {
				why := "este agente não altera configurações da BIOS (só o agente do Windows, com a caixa de cifragem)"
				a.Logger.Warn("job recusado", "job_id", j.ID, "reason", why)
				a.queueJobResult(state, j.ID, protocol.JobResultRejected, why)
				continue
			}
			a.Logger.Info("job aceito: alterar configurações da BIOS", "job_id", j.ID, "fabricante", p.Vendor, "alteracoes", len(p.Changes))
			acc.biosSettings = append(acc.biosSettings, acceptedBIOSSettings{id: j.ID, p: p})
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

// runWinUpdates instala as atualizações dos jobs windows_update, um job por vez (Aula
// 16.1). Os resultados vão depois da busca, que mostra o efeito ao servidor.
func (a *Agent) runWinUpdates(ctx context.Context, list []acceptedWinUpdate) []PendingJobResult {
	var out []PendingJobResult
	for _, j := range list {
		a.Logger.Info("instalação de atualizações do Windows iniciada", "job_id", j.id, "updates", strings.Join(j.p.Labels(), ","))
		start := a.now()
		detail, err := a.WinUpdate(ctx, j.id, j.p.Updates)
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrNotPending):
			status, detail = protocol.JobResultRejected, joinDetail(err.Error(), detail)
			a.Logger.Warn("job recusado", "job_id", j.id, "reason", detail)
		case errors.Is(err, jobs.ErrRetry):
			status, detail = protocol.JobResultPostponed, joinDetail(err.Error(), detail)
			a.Logger.Warn("instalação de atualizações do Windows adiada", "job_id", j.id, "reason", detail)
		case err != nil:
			status, detail = protocol.JobResultFailed, joinDetail(err.Error(), detail)
			a.Logger.Error("instalação de atualizações do Windows falhou", "job_id", j.id, "error", detail)
		default:
			a.Logger.Info("instalação de atualizações do Windows concluída", "job_id", j.id, "detail", detail,
				"duration", a.now().Sub(start).Round(time.Second).String())
		}
		out = append(out, PendingJobResult{ID: j.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out
}

// runThirdParty executa os jobs thirdparty, um por vez (Aula 14.4). ran diz se algum
// instalador chegou a rodar: só então o inventário pode ter mudado (Aula 14.5).
func (a *Agent) runThirdParty(ctx context.Context, list []acceptedThirdParty) (out []PendingJobResult, ran bool) {
	for _, j := range list {
		a.Logger.Info("instalação de terceiros iniciada", "job_id", j.id, "app", j.p.App, "version", j.p.Version)
		start := a.now()
		detail, err := a.ThirdParty(ctx, j.id, j.p)
		if !errors.Is(err, jobs.ErrRefused) && !errors.Is(err, jobs.ErrNotStarted) {
			ran = true
		}
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrRefused):
			status, detail = protocol.JobResultRejected, joinDetail(err.Error(), detail)
			a.Logger.Warn("job recusado", "job_id", j.id, "reason", detail)
		case errors.Is(err, jobs.ErrRetry):
			// Aula 14.9: programa aberto, outro instalador rodando: não é falha, é "agora não".
			status, detail = protocol.JobResultPostponed, joinDetail(err.Error(), detail)
			a.Logger.Warn("instalação de terceiros adiada", "job_id", j.id, "app", j.p.App, "reason", detail)
		case err != nil:
			status, detail = protocol.JobResultFailed, joinDetail(err.Error(), detail)
			a.Logger.Error("instalação de terceiros falhou", "job_id", j.id, "app", j.p.App, "error", detail)
		default:
			a.Logger.Info("instalação de terceiros concluída", "job_id", j.id, "app", j.p.App, "detail", detail,
				"duration", a.now().Sub(start).Round(time.Second).String())
		}
		out = append(out, PendingJobResult{ID: j.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out, ran
}

// runScripts executa os bundles de script (Aula 23.8), um por vez. ran diz se algum script
// chegou a rodar: só então o inventário de depois interessa (um script pode ter instalado
// ou removido programa).
func (a *Agent) runScripts(ctx context.Context, list []acceptedScript) (out []PendingJobResult, ran bool) {
	for _, j := range list {
		a.Logger.Info("execução de script iniciada", "job_id", j.id, "bundle", j.p.Bundle, "entry", j.p.Entry)
		start := a.now()
		detail, err := a.Script(ctx, j.id, j.p)
		if !errors.Is(err, jobs.ErrRefused) && !errors.Is(err, jobs.ErrNotStarted) {
			ran = true
		}
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrRefused):
			status, detail = protocol.JobResultRejected, joinDetail(err.Error(), detail)
			a.Logger.Warn("job recusado", "job_id", j.id, "reason", detail)
		case err != nil:
			status, detail = protocol.JobResultFailed, joinDetail(err.Error(), detail)
			a.Logger.Error("execução de script falhou", "job_id", j.id, "bundle", j.p.Bundle, "error", detail)
		default:
			a.Logger.Info("execução de script concluída", "job_id", j.id, "bundle", j.p.Bundle,
				"duration", a.now().Sub(start).Round(time.Second).String())
		}
		out = append(out, PendingJobResult{ID: j.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out, ran
}

// runBIOS grava a senha da BIOS (Aula 21.5), um job por vez. ran diz se o fabricante chegou
// a ser chamado: só então o inventário de depois interessa.
func (a *Agent) runBIOS(ctx context.Context, list []acceptedBIOS) (out []PendingJobResult, ran bool) {
	for _, j := range list {
		detail, err := a.BIOSPassword(ctx, j.id, j.p)
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrRefused):
			status, detail = protocol.JobResultRejected, err.Error()
			a.Logger.Warn("job recusado", "job_id", j.id, "reason", detail)
		case errors.Is(err, jobs.ErrBIOSUncertain):
			ran = true
			status, detail = protocol.JobResultFailed, jobs.BIOSUncertain+err.Error()
			a.Logger.Error("senha da BIOS: resultado incerto", "job_id", j.id, "error", err.Error())
		case err != nil:
			ran = true
			status, detail = protocol.JobResultFailed, err.Error()
			a.Logger.Error("senha da BIOS não gravada", "job_id", j.id, "error", detail)
		default:
			ran = true
			a.Logger.Info("senha da BIOS gravada", "job_id", j.id, "guarda", j.p.Guard, "detail", detail)
		}
		out = append(out, PendingJobResult{ID: j.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out, ran
}

// runBIOSSettings altera configurações da BIOS (docs/bios-config.md), um job por vez. ran diz
// se alguma escrita pode ter acontecido: só então o inventário de depois interessa.
func (a *Agent) runBIOSSettings(ctx context.Context, list []acceptedBIOSSettings) (out []PendingJobResult, ran bool) {
	for _, j := range list {
		detail, err := a.BIOSSettings(ctx, j.id, j.p)
		status := protocol.JobResultOK
		switch {
		case errors.Is(err, jobs.ErrRefused):
			status, detail = protocol.JobResultRejected, err.Error()
			a.Logger.Warn("job recusado", "job_id", j.id, "reason", detail)
		case err != nil:
			ran = true
			status, detail = protocol.JobResultFailed, err.Error()
			a.Logger.Error("configurações da BIOS: nem todas gravadas", "job_id", j.id, "detail", detail)
		default:
			ran = true
			a.Logger.Info("configurações da BIOS alteradas", "job_id", j.id, "detail", detail)
		}
		out = append(out, PendingJobResult{ID: j.id, Result: protocol.JobResult{Status: status, Detail: detail, FinishedAt: a.now().UTC()}})
	}
	return out, ran
}

// joinDetail junta o erro e o detalhe (o que o instalador disse antes de falhar).
func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// resendInventory coleta e envia o inventário de depois da instalação. Se não der, os
// resultados esperam o próximo ciclo, em que o inventário vai primeiro (InventoryFirst).
func (a *Agent) resendInventory(ctx context.Context, state *State) {
	inv, err := a.Inventory(ctx)
	if err != nil {
		a.Logger.Warn("inventário de depois da instalação não coletado; os resultados esperam o próximo ciclo", "error", err)
		state.InventoryFirst = true
		return
	}
	r, err := a.Client.SendInventory(ctx, inv)
	if out := a.classify(err, "envio do inventário de depois da instalação"); out != outcomeOK {
		state.InventoryFirst = true
		return
	}
	a.Logger.Info("inventário de depois da instalação enviado", "status", r.Status, "hash", r.Hash)
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
		// Já há um reinício agendado (Aula 17.6: o usuário escolhe a hora, e a TI pode pedir
		// outro). Vale o mais cedo: o mais tarde fica atendido por ele e volta dispensado.
		var replaced *PendingReboot
		if pr := state.PendingReboot; pr != nil {
			if !a.now().UTC().Add(r.delay).Before(pr.At) {
				reject(jobs.RebootAlreadyScheduled)
				continue
			}
			if a.AbortRestart == nil {
				reject(fmt.Sprintf("já há um reinício agendado (job %d)", pr.JobID))
				continue
			}
			replaced = pr
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
			reject(jobs.RebootNothingPending)
			continue
		case st.LastBoot == nil:
			reject("sem a hora do último boot, o agente não conseguiria conferir o reinício")
			continue
		}
		if replaced != nil {
			if err := a.AbortRestart(ctx); err != nil {
				a.Logger.Error("reinício anterior não cancelado; o novo fica de fora", "job_id", r.id, "anterior", replaced.JobID, "error", err)
				a.queueJobResult(state, r.id, protocol.JobResultFailed, fmt.Sprintf("não foi possível trocar o reinício do job %d por este, mais cedo: %v", replaced.JobID, err))
				continue
			}
			a.Logger.Info("reinício anterior cancelado: o novo é mais cedo", "job_id", r.id, "anterior", replaced.JobID)
			a.queueJobResult(state, replaced.JobID, protocol.JobResultRejected, jobs.RebootAlreadyScheduled)
			state.PendingReboot = nil
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
	a.clientMu.Lock() // o canal do autoatendimento (Aula 17.3) lê o endereço de outra goroutine
	a.Client.BaseURL = m.URL
	a.clientMu.Unlock()
}
