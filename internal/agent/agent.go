package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// maxClockSkew: diferença de relógio a partir da qual o agente avisa no log.
const maxClockSkew = 5 * time.Minute

// Agent é o laço de check-in. A cada ciclo: envia a busca pendente (se houver), faz o
// check-in com o hash do inventário (e só envia o inventário se o servidor não o tiver),
// e faz a busca de atualizações quando ela estiver devida.
type Agent struct {
	Client    *Client
	Inventory func(context.Context) (protocol.InventoryReport, error)
	Scan      func(context.Context) protocol.PatchScanReport
	// State coleta rede e discos para o check-in (Aula 12.1). nil: o check-in vai sem.
	State     func(context.Context) protocol.MachineState
	DataDir   string
	Interval  time.Duration // entre check-ins (RNF-03: 1 h)
	ScanEvery time.Duration // entre buscas (RNF-03: 24 h)
	Logger    *slog.Logger
	// Update recebe a versão oferecida no check-in (Aula 5.4); nil: o agente não se atualiza.
	Update func(ctx context.Context, version string)
	// OfflineCatalog recebe o catálogo offline anunciado no check-in (Aula 6.6), antes da
	// busca: uma busca devida neste ciclo já usa o arquivo novo. nil: anúncio ignorado.
	OfflineCatalog func(ctx context.Context, adv protocol.OfflineCatalog)
	// RepoCache recebe, a cada check-in, o cache de repositórios anunciado (nil: o servidor
	// não anuncia, e a configuração gravada antes deve sair). nil: o agente não mexe nisso.
	RepoCache func(ctx context.Context, adv *protocol.RepoCache)
	// MachineID e JobKey conferem os jobs (Aula 8.1): o job tem de ser desta máquina e
	// assinado pela chave de jobs embutida no build. JobKey nil: todo job é recusado.
	MachineID string
	JobKey    ed25519.PublicKey
	// JobKeyNext (Aula 20.7): a chave de jobs seguinte, aceita junto com a atual. nil: só a atual.
	JobKeyNext ed25519.PublicKey
	// Install aplica os pacotes de um job update (Aula 8.4). nil: o agente recusa esses
	// jobs (Windows e macOS, por enquanto). Erro com jobs.ErrNotPending vira "rejeitado".
	Install func(ctx context.Context, jobID int64, pkgs []jobs.Package) (string, error)
	// RebootStatus e Restart atendem o job reboot (Aula 8.6): o primeiro diz se há
	// reinício pendente e a hora do último boot; o segundo agenda o reinício com aviso aos
	// usuários. Restart nil: o agente recusa esses jobs.
	RebootStatus func(ctx context.Context) (protocol.RebootStatus, error)
	Restart      func(ctx context.Context, jobID int64, delay time.Duration) error
	// AbortRestart cancela o reinício agendado (Aula 17.6): com dois pedidos, vale o mais
	// cedo. nil: o segundo pedido é recusado, como antes.
	AbortRestart func(ctx context.Context) error
	// SaveServerURL grava o endereço novo do servidor na credencial (job server_url, Aula
	// 9.2). nil: o agente recusa esses jobs.
	SaveServerURL func(url string) error
	// ThirdParty executa o job thirdparty (Aula 14.4): baixa, confere e instala um programa
	// de terceiros. nil: o agente recusa esses jobs (só o Windows instala). Erro com
	// jobs.ErrRefused vira "rejeitado" (nada foi executado).
	ThirdParty func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error)
	// WinUpdate instala atualizações do Windows pelo WUA (job windows_update, Aula 16.1).
	// nil: o agente recusa esses jobs (só o Windows). Erro com jobs.ErrNotPending vira
	// "rejeitado"; com jobs.ErrRetry, "adiado". Nunca reinicia.
	WinUpdate func(ctx context.Context, jobID int64, updates []jobs.WinUpdate) (string, error)
	// Script executa o job script (Aula 23.8): baixa o bundle (ZIP), confere o SHA-256,
	// extrai e roda o ponto de entrada. nil: o agente recusa esses jobs. Erro com
	// jobs.ErrRefused vira "rejeitado" (nada foi executado na máquina).
	Script func(ctx context.Context, jobID int64, p jobs.ScriptParams) (string, error)
	// BIOSPassword grava a senha de administrador da BIOS (job bios_password, Aula 21.5).
	// nil: o agente recusa esses jobs. Erro com jobs.ErrRefused vira "rejeitado" (a BIOS não
	// foi tocada); com bioswrite.ErrUncertain, "falhou" com o detalhe jobs.BIOSUncertain.
	BIOSPassword func(ctx context.Context, jobID int64, p jobs.BIOSPasswordParams) (string, error)
	// BIOSSettings altera configurações da BIOS da lista fechada (job bios_settings,
	// docs/bios-config.md). nil: o agente recusa esses jobs. Erro com jobs.ErrRefused vira
	// "rejeitado" (a BIOS não foi tocada); outro erro, "falhou", com uma linha por configuração.
	BIOSSettings func(ctx context.Context, jobID int64, p jobs.BIOSSettingsParams) (string, error)

	// Watch: a sentinela de desempenho e de mudanças (docs/monitoramento.md). nil: sem
	// amostras de desempenho e sem check-in antecipado por mudança.
	Watch     *Watch
	watchOnce sync.Once
	ws        *watchState
	// wakeReason: o motivo do check-in antecipado (vai no check-in, para o log do servidor).
	wakeReason atomic.Value

	// wake antecipa o próximo check-in (Aula 17.3): um pedido do autoatendimento aceito
	// não espera a próxima hora. Capacidade 1: vários pedidos juntos dão um check-in só.
	wakeOnce sync.Once
	wake     chan struct{}
	// clientMu protege o endereço do Client, que o job server_url troca, das chamadas do
	// autoatendimento, que vêm de outra goroutine.
	clientMu sync.RWMutex

	// Injetáveis nos testes.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	rand  randFn
}

// outcome é o resultado de um ciclo, e decide a espera até o próximo.
type outcome int

const (
	outcomeOK           outcome = iota
	outcomeTransient            // rede ou servidor fora: nova tentativa com espera crescente
	outcomeUnauthorized         // credencial recusada: espera normal, sem insistir
	outcomeRejected             // conteúdo recusado: espera normal; o próximo ciclo coleta de novo
)

func (a *Agent) defaults() {
	if a.now == nil {
		a.now = time.Now
	}
	if a.sleep == nil {
		a.sleep = sleepCtx
	}
	if a.rand == nil {
		a.rand = rand.Int64N
	}
}

// sleepCtx espera d ou até o contexto acabar (SIGTERM, Ctrl+C).
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// MinWakeGap: o check-in antecipado nunca vem antes disso depois do anterior. Um job criado
// pelo pedido chega no próximo check-in; dois pedidos seguidos não viram uma rajada.
const MinWakeGap = 30 * time.Second

func (a *Agent) wakeChan() chan struct{} {
	a.wakeOnce.Do(func() { a.wake = make(chan struct{}, 1) })
	return a.wake
}

// Wake pede o próximo check-in agora (Aula 17.3). Não bloqueia; pode ser chamado de
// qualquer goroutine, antes ou durante o Run. Se um check-in estiver em andamento, o
// seguinte vem logo depois dele (respeitando o MinWakeGap).
func (a *Agent) Wake() { a.wakeFor(ReasonSelfService) }

// wakeFor é o Wake com o motivo.
func (a *Agent) wakeFor(reason string) {
	a.wakeReason.Store(reason)
	select {
	case a.wakeChan() <- struct{}{}:
	default:
	}
}

// waitTurn espera d, ou menos se o Wake for chamado. woke diz se foi antecipado.
func (a *Agent) waitTurn(ctx context.Context, d time.Duration) (woke bool, err error) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var w atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-a.wakeChan():
			w.Store(true)
			cancel()
		case <-done:
		}
	}()
	_ = a.sleep(wctx, d)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return w.Load(), nil
}

// Run executa o laço até o contexto acabar. Encerramento pedido não é erro.
func (a *Agent) Run(ctx context.Context) error {
	a.defaults()
	wait := initialDelay(a.rand, a.Interval)
	a.Logger.Info("laço de check-in iniciado", "first_checkin_in", wait.Round(time.Second).String(),
		"interval", a.Interval.String(), "scan_every", a.ScanEvery.String())

	if a.Watch != nil {
		go a.watchLoop(ctx)
	}
	failures := 0
	var last time.Time
	for {
		woke, err := a.waitTurn(ctx, wait)
		if err != nil {
			return nil
		}
		if woke {
			if gap := MinWakeGap - a.now().Sub(last); !last.IsZero() && gap > 0 {
				if err := a.sleep(ctx, gap); err != nil {
					return nil
				}
			}
			reason, _ := a.wakeReason.Load().(string)
			a.Logger.Info("check-in antecipado", "motivo", reason)
		} else {
			a.wakeReason.Store("")
		}
		out := a.CheckIn(ctx)
		last = a.now()
		if ctx.Err() != nil {
			return nil
		}
		if out == outcomeTransient {
			failures++
			wait = backoff(a.rand, failures, a.Interval)
		} else {
			failures = 0
			wait = nextCheckin(a.rand, a.Interval)
		}
		a.Logger.Debug("próximo check-in agendado", "in", wait.Round(time.Second).String(), "consecutive_failures", failures)
	}
}

// CheckIn executa um ciclo completo e devolve o resultado.
func (a *Agent) CheckIn(ctx context.Context) outcome {
	a.defaults()
	start := a.now()

	// 1. Uma busca que ficou para trás vai primeiro: ela levou minutos para ser feita.
	pending, err := loadPendingScan(a.DataDir)
	if err != nil {
		a.Logger.Warn("busca pendente ilegível", "error", err)
	}
	if pending != nil {
		if out := a.sendScan(ctx, *pending, "busca pendente"); out != outcomeOK {
			return out
		}
	}

	// Resultados de jobs que ficaram para trás, depois da busca pendente: o servidor confere
	// o rescan pela busca recebida, então ela tem de chegar antes do resultado.
	state, err := loadState(a.DataDir)
	if err != nil {
		a.Logger.Warn("estado do agente ilegível; recomeçando a agenda", "error", err)
	}
	if len(state.PendingJobResults) > 0 && !state.InventoryFirst {
		out := a.flushJobResults(ctx, &state)
		a.saveStateOrLog(state)
		if out != outcomeOK {
			return out
		}
	}

	// Reinício agendado por job (Aula 8.6): se a máquina já voltou, a busca deste ciclo
	// confirma o efeito e o resultado vai depois dela.
	rebooted := a.resolveReboot(ctx, &state)
	if state.PendingReboot == nil && rebooted != 0 {
		a.saveStateOrLog(state)
	}

	// 2. Check-in com o hash do inventário; o inventário só vai se o servidor não o tiver.
	inv, err := a.Inventory(ctx)
	if err != nil {
		a.Logger.Error("falha na coleta do inventário; check-in sem inventário", "error", err)
		inv = protocol.InventoryReport{}
	}
	// Rede e discos (Aula 12.1) vão no próprio check-in: são poucos bytes, e o servidor
	// guarda só o último. nil: o agente não coleta (testes, SO sem coletor).
	var host *protocol.MachineState
	if a.State != nil {
		s := a.State(ctx)
		host = &s
		if len(s.Errors) > 0 {
			a.Logger.Warn("coleta de rede e discos incompleta", "errors", s.Errors)
		}
	}
	perf := a.perfBatch()
	reason, _ := a.wakeReason.Load().(string)
	resp, err := a.Client.CheckInWith(ctx, inv.Hash, host, perf, reason)
	if out := a.classify(err, "check-in"); out != outcomeOK {
		return out
	}
	a.wakeReason.Store("")
	a.perfSent(len(perf), resp.Monitor)
	a.checkClock(resp.ServerTime)
	inventorySent := false
	if !resp.InventoryKnown && inv.Hash != "" {
		r, err := a.Client.SendInventory(ctx, inv)
		if out := a.classify(err, "envio do inventário"); out != outcomeOK {
			return out
		}
		inventorySent = true
		a.Logger.Info("inventário enviado", "status", r.Status, "hash", r.Hash, "software", len(inv.Software))
	}
	if state.InventoryFirst && (inventorySent || resp.InventoryKnown) {
		state.InventoryFirst = false // o inventário de depois da instalação já está no servidor
		a.saveStateOrLog(state)
	}

	// 3. Catálogo offline anunciado pelo servidor (só o agente do Windows o usa). Uma falha
	// não interrompe o ciclo: a busca segue com o catálogo anterior, e o download continua
	// de onde parou no próximo check-in.
	if resp.OfflineCatalog != nil && a.OfflineCatalog != nil {
		a.OfflineCatalog(ctx, *resp.OfflineCatalog)
		if ctx.Err() != nil {
			return outcomeTransient
		}
	}

	// O cache de repositórios vem antes da busca: o apt e o dnf da busca já passam por ele.
	if a.RepoCache != nil {
		a.RepoCache(ctx, resp.RepoCache)
		if ctx.Err() != nil {
			return outcomeTransient
		}
	}

	// Jobs (Aula 8.1): conferidos e aceitos antes da busca, que um rescan antecipa. O
	// estado é gravado já aqui: um job aceito não roda de novo nem se o agente cair agora.
	accepted := a.acceptJobs(resp.Jobs, &state)
	if len(resp.Jobs) > 0 {
		a.saveStateOrLog(state)
	}
	// Instalações (Aula 8.4) antes da busca: a busca seguinte mostra o efeito. O job já foi
	// gravado como visto: se o agente cair no meio, ele não roda de novo (no máximo uma vez).
	updateResults := a.runUpdates(ctx, accepted.updates)
	// Atualizações do Windows (Aula 16.1): mesma regra; o resultado vai depois da busca.
	updateResults = append(updateResults, a.runWinUpdates(ctx, accepted.winUpdates)...)
	if ctx.Err() != nil {
		state.PendingJobResults = append(state.PendingJobResults, updateResults...)
		if rebooted != 0 {
			a.queueJobResult(&state, rebooted, protocol.JobResultOK, "máquina reiniciada; busca feita depois do boot")
		}
		a.saveStateOrLog(state)
		return outcomeTransient
	}

	// Programas de terceiros (Aula 14.4): depois das instalações do sistema; o inventário
	// de depois vai antes dos resultados, que o servidor confere por ele.
	if len(accepted.thirdParty) > 0 {
		res, ran := a.runThirdParty(ctx, accepted.thirdParty)
		state.PendingJobResults = append(state.PendingJobResults, res...)
		if ctx.Err() != nil {
			state.InventoryFirst = state.InventoryFirst || ran
			a.saveStateOrLog(state)
			return outcomeTransient
		}
		// Nenhum instalador rodou (download falhou, recusa): o inventário não mudou.
		if ran {
			a.resendInventory(ctx, &state)
		}
		a.saveStateOrLog(state)
	}

	// Bundles de script (Aula 23.8): depois das instalações. Um script pode ter instalado ou
	// removido programa, então o inventário de depois vai antes dos resultados.
	if len(accepted.scripts) > 0 {
		res, ran := a.runScripts(ctx, accepted.scripts)
		state.PendingJobResults = append(state.PendingJobResults, res...)
		if ctx.Err() != nil {
			state.InventoryFirst = state.InventoryFirst || ran
			a.saveStateOrLog(state)
			return outcomeTransient
		}
		if ran {
			a.resendInventory(ctx, &state)
		}
		a.saveStateOrLog(state)
	}

	// Senha da BIOS (Aula 21.5): depois das instalações; o inventário de depois (o firmware
	// com a senha definida) vai antes do resultado, que o servidor confere por ele.
	if len(accepted.bios) > 0 {
		res, ran := a.runBIOS(ctx, accepted.bios)
		state.PendingJobResults = append(state.PendingJobResults, res...)
		if ctx.Err() != nil {
			state.InventoryFirst = state.InventoryFirst || ran
			a.saveStateOrLog(state)
			return outcomeTransient
		}
		if ran {
			a.resendInventory(ctx, &state)
		}
		a.saveStateOrLog(state)
	}

	// Configurações da BIOS (docs/bios-config.md): depois da senha; o inventário de depois
	// (as configurações com os valores novos) vai antes do resultado, que o servidor confere.
	if len(accepted.biosSettings) > 0 {
		res, ran := a.runBIOSSettings(ctx, accepted.biosSettings)
		state.PendingJobResults = append(state.PendingJobResults, res...)
		if ctx.Err() != nil {
			state.InventoryFirst = state.InventoryFirst || ran
			a.saveStateOrLog(state)
			return outcomeTransient
		}
		if ran {
			a.resendInventory(ctx, &state)
		}
		a.saveStateOrLog(state)
	}

	// Reinício pedido: agendado depois das instalações (o aviso corre enquanto a busca e
	// os envios terminam). O estado é gravado já: se o agente cair, ele sabe que esperar.
	if len(accepted.reboots) > 0 {
		a.scheduleReboots(ctx, &state, accepted.reboots)
		a.saveStateOrLog(state)
	}

	// 4. Busca de atualizações, quando devida ou pedida por um job.
	scanSent := false
	byJob := accepted.forceScan() || rebooted != 0
	if !a.now().Before(state.NextScanAt) || byJob {
		a.Logger.Info("busca de atualizações iniciada", "por_job", byJob)
		rep := a.Scan(ctx)
		if ctx.Err() != nil {
			return outcomeTransient
		}
		state.LastScanAt = rep.ScannedAt
		if state.LastScanAt.IsZero() {
			state.LastScanAt = a.now().UTC()
		}
		state.NextScanAt = nextScanAt(a.rand, state.LastScanAt, a.ScanEvery).UTC()
		if err := saveState(a.DataDir, state); err != nil {
			a.Logger.Error("não foi possível gravar a agenda; a busca pode se repetir no próximo reinício", "error", err)
		}
		if err := savePendingScan(a.DataDir, rep); err != nil {
			a.Logger.Error("não foi possível guardar a busca no disco", "error", err)
		}
		// O resultado do rescan fica guardado junto com a busca: se o envio falhar agora,
		// os dois vão no próximo ciclo, a busca primeiro.
		for _, id := range accepted.rescans {
			a.queueJobResult(&state, id, protocol.JobResultOK, "inventário e busca de atualizações enviados")
		}
		state.PendingJobResults = append(state.PendingJobResults, updateResults...)
		if rebooted != 0 {
			a.queueJobResult(&state, rebooted, protocol.JobResultOK, "máquina reiniciada; busca feita depois do boot")
		}
		a.saveStateOrLog(state)
		a.Logger.Info("busca de atualizações concluída", "sources", len(rep.Scans),
			"duration", a.now().Sub(start).Round(time.Second).String(), "next_scan_at", state.NextScanAt)
		if out := a.sendScan(ctx, rep, "busca"); out != outcomeOK {
			return out
		}
		scanSent = true
	}

	// Troca de endereço (Aula 9.2), com o ciclo já entregue pelo endereço atual: o
	// resultado vai pelo endereço novo e, se chegar, o agente passa a usá-lo. Uma falha
	// vira resultado "falhou", entregue logo abaixo pelo endereço atual.
	if state.PendingServerMove != nil {
		a.moveServer(ctx, &state)
		a.saveStateOrLog(state)
		if ctx.Err() != nil {
			return outcomeTransient
		}
	}

	// Resultados dos jobs deste ciclo (e as recusas), depois da busca.
	if len(state.PendingJobResults) > 0 && !state.InventoryFirst {
		out := a.flushJobResults(ctx, &state)
		a.saveStateOrLog(state)
		if out != outcomeOK {
			return out
		}
	}

	a.Logger.Info("check-in concluído", "inventory_sent", inventorySent, "scan_sent", scanSent,
		"duration", a.now().Sub(start).Round(time.Millisecond).String())

	// 5. Atualização do agente, por último: os relatórios deste ciclo já foram entregues.
	if resp.AgentUpdate != nil && a.Update != nil {
		a.Update(ctx, resp.AgentUpdate.Version)
	}
	return outcomeOK
}

func (a *Agent) saveStateOrLog(s State) {
	if err := saveState(a.DataDir, s); err != nil {
		a.Logger.Error("não foi possível gravar o estado do agente", "error", err)
	}
}

// sendScan envia uma busca e remove a pendência quando o servidor a aceita ou recusa
// (recusada, ela não vai passar nunca: guardá-la travaria todos os ciclos seguintes).
func (a *Agent) sendScan(ctx context.Context, rep protocol.PatchScanReport, what string) outcome {
	_, err := a.Client.SendScan(ctx, rep)
	out := a.classify(err, "envio da "+what)
	if out == outcomeOK || out == outcomeRejected {
		if err := removePendingScan(a.DataDir); err != nil {
			a.Logger.Warn("não foi possível remover a busca pendente", "error", err)
		}
	}
	if out == outcomeOK {
		a.Logger.Info(what+" enviada", "scanned_at", rep.ScannedAt)
	}
	return out
}

// classify registra a falha no nível certo e a converte em resultado do ciclo.
func (a *Agent) classify(err error, what string) outcome {
	var rejected *RejectedError
	var transient *TransientError
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, ErrUnauthorized):
		a.Logger.Error(what+": credencial recusada; registre a máquina de novo (patchd-agent enroll)", "error", err)
		return outcomeUnauthorized
	case errors.As(err, &rejected):
		a.Logger.Error(what+": conteúdo recusado pelo servidor", "error", err)
		return outcomeRejected
	case errors.As(err, &transient):
		a.Logger.Warn(what+": servidor indisponível; nova tentativa com espera crescente", "error", err)
		return outcomeTransient
	default:
		a.Logger.Warn(what+" interrompido", "error", err)
		return outcomeTransient
	}
}

// checkClock avisa quando o relógio desta máquina diverge do servidor.
func (a *Agent) checkClock(server time.Time) {
	if server.IsZero() {
		return
	}
	if skew := a.now().Sub(server); skew > maxClockSkew || skew < -maxClockSkew {
		a.Logger.Warn("relógio desta máquina divergente do servidor", "skew_seconds", int64(skew.Seconds()))
	}
}
