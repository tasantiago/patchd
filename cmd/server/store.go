package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/store"
)

// Os dois armazenamentos cumprem a interface da API: conferido na compilação.
var (
	_ api.Store = (*store.Memory)(nil)
	_ api.Store = (*store.Postgres)(nil)
)

// dbWait é quanto o servidor espera o PostgreSQL responder na partida.
const dbWait = 30 * time.Second

// openStore escolhe o armazenamento: PostgreSQL quando há URL do banco (conecta, aplica
// as migrations e só então devolve), memória quando não há (só para desenvolvimento).
// A função devolvida fecha o pool no encerramento.
func openStore(ctx context.Context, logger *slog.Logger, databaseURL string) (api.Store, func(), error) {
	if databaseURL == "" {
		logger.Warn("PATCHD_DATABASE_URL não definido: armazenamento em memória, os relatórios se perdem ao reiniciar")
		return store.NewMemory(), func() {}, nil
	}

	start := time.Now()
	waited := false
	pool, err := store.ConnectNotify(ctx, databaseURL, dbWait, func(err error) {
		waited = true
		logger.Warn("aguardando o banco", "espera_max", dbWait.String(), "error", err)
	})
	if err != nil {
		return nil, nil, err
	}
	if waited {
		logger.Info("banco respondeu", "depois_de", time.Since(start).Round(time.Second).String())
	}
	if err := store.Migrate(ctx, pool, logger); err != nil {
		pool.Close()
		return nil, nil, err
	}
	logger.Info("armazenamento no PostgreSQL")
	return store.NewPostgres(pool), pool.Close, nil
}
