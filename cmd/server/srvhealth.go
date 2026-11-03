package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/srvhealth"
	"github.com/tasantiago/patchd/internal/store"
)

// startServerHealth liga a medição do próprio servidor (docs/saude-servidor.md). As pastas de
// dados medidas são as configuradas; o banco, se for o PostgreSQL, guarda as horas.
func startServerHealth(ctx context.Context, logger *slog.Logger, st api.Store, cfg serverConfig, look config.Lookup) *srvhealth.Monitor {
	var paths []srvhealth.Path
	add := func(name, dir string) {
		if dir != "" {
			paths = append(paths, srvhealth.Path{Name: name, Dir: dir})
		}
	}
	if dir, err := contentDir(look); err == nil {
		add("Conteúdo (catálogo e pacotes)", dir)
	}
	add("Cache de repositórios", cfg.RepoCacheDir)
	add("Versões do agente", cfg.ReleasesDir)
	var keeper srvhealth.Keeper
	if pg, ok := st.(*store.Postgres); ok {
		keeper = pg
	}
	m := srvhealth.New(keeper, paths, logger)
	go m.Run(ctx)
	return m
}
