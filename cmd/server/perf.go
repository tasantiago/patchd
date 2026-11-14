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
	PerfLastHour(ctx context.Context) (time.Time, error)
	PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error)
	// PruneTerminal: o registro do terminal do administrador sai depois de store.TerminalKeep.
	PruneTerminal(ctx context.Context, before time.Time) (int64, error)
}

// perfState: o que a passada lembra de uma para a outra.
type perfState struct {
	rolled      bool      // alguma passada já montou as horas desde a partida
	hoursPruned time.Time // a última limpeza das horas (uma por dia)
}

// perfHoursPruneEvery: a limpeza das horas de mais de 90 dias varre a tabela inteira (milhões
// de linhas, sem índice pela hora, que não vale o custo): uma vez por dia basta. As amostras
// e os alertas têm índice pela data e seguem de hora em hora.
const perfHoursPruneEvery = 24 * time.Hour

// perfPass monta as horas fechadas e aplica os prazos.
//
// A passada regular refaz só as horas recentes e as marcadas por amostra atrasada: com 4.000
// máquinas, refazer as 48 horas a cada hora eram 20 s de banco e 80 mil linhas regravadas.
// A primeira depois da partida cobre o tempo em que o servidor ficou parado: desde a última
// hora montada (o servidor que só reiniciou refaz poucas horas, não as 48: eram 37 s de banco
// a cada reinício com 7.000 máquinas), e as 48 inteiras só quando não há hora nenhuma ou a
// parada foi longa.
func perfPass(ctx context.Context, logger *slog.Logger, k perfKeeper, now time.Time, st *perfState) {
	hour := now.UTC().Truncate(time.Hour)
	back := store.PerfLateAfter + time.Hour
	if !st.rolled {
		regular := back
		back = perfRollupBack
		if last, err := k.PerfLastHour(ctx); err == nil && !last.IsZero() && !last.After(hour) {
			back = min(perfRollupBack, hour.Sub(last)+regular)
		}
	}
	_, err := k.RollupPerf(ctx, hour.Add(-back), hour)
	if err == nil {
		_, err = k.RollupLatePerf(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("desempenho: montar as horas", "error", err)
		}
		return
	}
	st.rolled = true
	var hoursBefore time.Time // zero: nesta passada, as horas não são limpas
	if now.Sub(st.hoursPruned) >= perfHoursPruneEvery {
		hoursBefore = now.Add(-perfHoursKeep)
	}
	n, err := k.PrunePerf(ctx, now.Add(-perfSamplesKeep), hoursBefore, now.Add(-perfAlertsKeep))
	switch {
	case err != nil && ctx.Err() == nil:
		logger.Error("desempenho: retenção", "error", err)
	case err == nil && !hoursBefore.IsZero():
		st.hoursPruned = now
	}
	if n > 0 {
		logger.Info("desempenho: linhas antigas apagadas", "linhas", n)
	}
	if n, err := k.PruneTerminal(ctx, now.Add(-store.TerminalKeep)); err != nil && ctx.Err() == nil {
		logger.Error("terminal: retenção", "error", err)
	} else if n > 0 {
		logger.Info("terminal: comandos com mais de 365 dias apagados", "linhas", n)
	}
}

// perfLoop roda a passada na partida e depois de hora em hora.
func perfLoop(ctx context.Context, logger *slog.Logger, k perfKeeper) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	st := &perfState{}
	for {
		perfPass(ctx, logger, k, time.Now(), st)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
