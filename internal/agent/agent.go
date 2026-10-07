package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

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
	DataDir   string
	Interval  time.Duration // entre check-ins (RNF-03: 1 h)
	ScanEvery time.Duration // entre buscas (RNF-03: 24 h)
	Logger    *slog.Logger
	// Update recebe a versão oferecida no check-in (Aula 5.4); nil: o agente não se atualiza.
	Update func(ctx context.Context, version string)
	// OfflineCatalog recebe o catálogo offline anunciado no check-in (Aula 6.6), antes da
	// busca: uma busca devida neste ciclo já usa o arquivo novo. nil: anúncio ignorado.
	OfflineCatalog func(ctx context.Context, adv protocol.OfflineCatalog)

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

// Run executa o laço até o contexto acabar. Encerramento pedido não é erro.
func (a *Agent) Run(ctx context.Context) error {
	a.defaults()
	wait := initialDelay(a.rand, a.Interval)
	a.Logger.Info("laço de check-in iniciado", "first_checkin_in", wait.Round(time.Second).String(),
		"interval", a.Interval.String(), "scan_every", a.ScanEvery.String())

	failures := 0
	for {
		if err := a.sleep(ctx, wait); err != nil {
			return nil
		}
		out := a.CheckIn(ctx)
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

	// 2. Check-in com o hash do inventário; o inventário só vai se o servidor não o tiver.
	inv, err := a.Inventory(ctx)
	if err != nil {
		a.Logger.Error("falha na coleta do inventário; check-in sem inventário", "error", err)
		inv = protocol.InventoryReport{}
	}
	resp, err := a.Client.CheckIn(ctx, inv.Hash)
	if out := a.classify(err, "check-in"); out != outcomeOK {
		return out
	}
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

	// 3. Catálogo offline anunciado pelo servidor (só o agente do Windows o usa). Uma falha
	// não interrompe o ciclo: a busca segue com o catálogo anterior, e o download continua
	// de onde parou no próximo check-in.
	if resp.OfflineCatalog != nil && a.OfflineCatalog != nil {
		a.OfflineCatalog(ctx, *resp.OfflineCatalog)
		if ctx.Err() != nil {
			return outcomeTransient
		}
	}

	// 4. Busca de atualizações, quando devida.
	state, err := loadState(a.DataDir)
	if err != nil {
		a.Logger.Warn("estado do agente ilegível; recomeçando a agenda", "error", err)
	}
	scanSent := false
	if !a.now().Before(state.NextScanAt) {
		a.Logger.Info("busca de atualizações iniciada")
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
		a.Logger.Info("busca de atualizações concluída", "sources", len(rep.Scans),
			"duration", a.now().Sub(start).Round(time.Second).String(), "next_scan_at", state.NextScanAt)
		if out := a.sendScan(ctx, rep, "busca"); out != outcomeOK {
			return out
		}
		scanSent = true
	}

	a.Logger.Info("check-in concluído", "inventory_sent", inventorySent, "scan_sent", scanSent,
		"duration", a.now().Sub(start).Round(time.Millisecond).String())

	// 5. Atualização do agente, por último: os relatórios deste ciclo já foram entregues.
	if resp.AgentUpdate != nil && a.Update != nil {
		a.Update(ctx, resp.AgentUpdate.Version)
	}
	return outcomeOK
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
