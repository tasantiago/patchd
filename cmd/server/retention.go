package main

import (
	"context"
	"log/slog"
	"time"
)

const (
	// scanRetention: buscas mais velhas que isso são apagadas, exceto a atual de cada
	// máquina. 90 dias cobrem um trimestre de auditoria com folga; com 1500 máquinas e uma
	// busca por dia, são cerca de 135 mil linhas.
	scanRetention = 90 * 24 * time.Hour
	pruneEvery    = time.Hour
)

// scanPruner é o armazenamento que sabe apagar buscas antigas (o PostgreSQL; a memória
// guarda só a última e não precisa).
type scanPruner interface {
	PruneScans(ctx context.Context, before time.Time) (int64, error)
}

// pruneScansLoop aplica a retenção na partida e depois de hora em hora, até o encerramento.
func pruneScansLoop(ctx context.Context, logger *slog.Logger, p scanPruner) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		before := time.Now().Add(-scanRetention)
		n, err := p.PruneScans(ctx, before)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("retenção das buscas falhou", "error", err)
		case n > 0:
			logger.Info("buscas antigas apagadas", "count", n, "before", before.UTC().Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
