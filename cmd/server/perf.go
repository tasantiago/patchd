package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// Desempenho (docs/monitoramento.md, seção 5): de hora em hora, as amostras viram médias e
// picos por hora, e o que passou do prazo sai.
const (
	// perfSamplesKeep: as amostras de minuto só servem à curva de 24 horas e à montagem das
	// horas (as últimas perfRollupBack); 7 dias delas eram ~3,6 GB num parque de 2.000
	// máquinas sem uso nenhum (as curvas de 7 e 90 dias leem as horas).
	perfSamplesKeep = perfRollupBack
	perfHoursKeep   = 90 * 24 * time.Hour
	perfAlertsKeep  = 90 * 24 * time.Hour
	// perfRollupBack: as horas refeitas na primeira passada (uma amostra pode chegar até 24 horas
	// atrasada, no check-in seguinte de uma máquina que ficou fora da rede).
	perfRollupBack = 48 * time.Hour
)

type perfKeeper interface {
	RollupPerf(ctx context.Context, from, to time.Time) (int64, error)
	RollupLatePerf(ctx context.Context) (int64, error)
	PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error)
	// PruneTerminal: o registro do terminal do administrador sai depois de store.TerminalKeep.
	PruneTerminal(ctx context.Context, before time.Time) (int64, error)
}

// perfPass monta as horas fechadas e aplica os prazos, e diz se as horas ficaram montadas.
// full: refaz as últimas perfRollupBack inteiras (na partida: o servidor pode ter ficado
// parado e perdido passadas). Nas outras, só as horas recentes e as marcadas por amostra
// atrasada: com 4.000 máquinas, refazer as 48 horas a cada hora eram 20 s de banco e 80 mil
// linhas regravadas por passada.
func perfPass(ctx context.Context, logger *slog.Logger, k perfKeeper, now time.Time, full bool) bool {
	hour := now.UTC().Truncate(time.Hour)
	back := store.PerfLateAfter + time.Hour
	if full {
		back = perfRollupBack
	}
	_, err := k.RollupPerf(ctx, hour.Add(-back), hour)
	if err == nil {
		_, err = k.RollupLatePerf(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("desempenho: montar as horas", "error", err)
		}
		return false
	}
	n, err := k.PrunePerf(ctx, now.Add(-perfSamplesKeep), now.Add(-perfHoursKeep), now.Add(-perfAlertsKeep))
	switch {
	case err != nil && ctx.Err() == nil:
		logger.Error("desempenho: retenção", "error", err)
	case n > 0:
		logger.Info("desempenho: linhas antigas apagadas", "linhas", n)
	}
	if n, err := k.PruneTerminal(ctx, now.Add(-store.TerminalKeep)); err != nil && ctx.Err() == nil {
		logger.Error("terminal: retenção", "error", err)
	} else if n > 0 {
		logger.Info("terminal: comandos com mais de 365 dias apagados", "linhas", n)
	}
	return true
}

// perfLoop roda a passada na partida e depois de hora em hora.
func perfLoop(ctx context.Context, logger *slog.Logger, k perfKeeper) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	full := true // até a primeira passada que montar as horas
	for {
		if perfPass(ctx, logger, k, time.Now(), full) {
			full = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
