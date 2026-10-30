package main

import (
	"context"
	"log/slog"
	"time"
)

// Desempenho (docs/monitoramento.md, seção 5): de hora em hora, as amostras viram médias e
// picos por hora, e o que passou do prazo sai.
const (
	perfSamplesKeep = 7 * 24 * time.Hour
	perfHoursKeep   = 90 * 24 * time.Hour
	perfAlertsKeep  = 90 * 24 * time.Hour
	// perfRollupBack: as horas refeitas a cada passada (uma amostra pode chegar até 24 horas
	// atrasada, no check-in seguinte de uma máquina que ficou fora da rede).
	perfRollupBack = 48 * time.Hour
)

type perfKeeper interface {
	RollupPerf(ctx context.Context, from, to time.Time) (int64, error)
	PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error)
}

// perfPass monta as horas fechadas e aplica os prazos.
func perfPass(ctx context.Context, logger *slog.Logger, k perfKeeper, now time.Time) {
	hour := now.UTC().Truncate(time.Hour)
	if _, err := k.RollupPerf(ctx, hour.Add(-perfRollupBack), hour); err != nil {
		if ctx.Err() == nil {
			logger.Error("desempenho: montar as horas", "error", err)
		}
		return
	}
	n, err := k.PrunePerf(ctx, now.Add(-perfSamplesKeep), now.Add(-perfHoursKeep), now.Add(-perfAlertsKeep))
	switch {
	case err != nil && ctx.Err() == nil:
		logger.Error("desempenho: retenção", "error", err)
	case n > 0:
		logger.Info("desempenho: linhas antigas apagadas", "linhas", n)
	}
}

// perfLoop roda a passada na partida e depois de hora em hora.
func perfLoop(ctx context.Context, logger *slog.Logger, k perfKeeper) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		perfPass(ctx, logger, k, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
