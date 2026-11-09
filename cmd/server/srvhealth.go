package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/srvhealth"
	"github.com/tasantiago/patchd/internal/store"
)

// startServerHealth liga a medição do próprio servidor (docs/saude-servidor.md). As pastas de
// dados medidas são as configuradas; o banco, se for o PostgreSQL, guarda as horas.
func startServerHealth(ctx context.Context, logger *slog.Logger, st api.Store, cfg serverConfig, look config.Lookup) *srvhealth.Monitor {
	var paths []srvhealth.Path
	// Nos containers (compose e Podman), o conteúdo e o cache de repositórios são volumes
	// nomeados, no mesmo armazenamento do volume do PostgreSQL: a previsão do disco usa eles.
	// As versões do agente são uma pasta do host (no Podman, no /home), que pode ser outra.
	container := inContainer()
	add := func(name, dir string, db bool) {
		if dir != "" {
			paths = append(paths, srvhealth.Path{Name: name, Dir: dir, DB: db && container})
		}
	}
	if dir, err := contentDir(look); err == nil {
		add("Conteúdo (catálogo e pacotes)", dir, true)
	}
	add("Cache de repositórios", cfg.RepoCacheDir, true)
	add("Versões do agente", cfg.ReleasesDir, false)
	var keeper srvhealth.Keeper
	if pg, ok := st.(*store.Postgres); ok {
		keeper = pg
	}
	m := srvhealth.New(keeper, paths, logger)
	go m.Run(ctx)
	return m
}

// inContainer: o servidor roda num container (Podman ou Docker). Variável para os testes.
var inContainer = func() bool {
	for _, f := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}
