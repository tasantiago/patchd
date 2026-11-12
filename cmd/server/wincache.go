package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/wucache"
)

// O cache das atualizações do Windows (docs/cache-windows.md) fica na pasta de conteúdo, ao
// lado do catálogo offline e dos instaladores de terceiros: no Podman, o volume que já existe.

// winCacheDir: a pasta do cache dentro de PATCHD_CONTENT_DIR.
func winCacheDir(content string) string { return filepath.Join(content, "windows-update") }

// newWinCache monta o cache. nil: sem PATCHD_CONTENT_DIR (o cache fica desligado).
func newWinCache(look config.Lookup, version string, logger *slog.Logger) *wucache.Cache {
	dir, err := contentDir(look)
	if err != nil || dir == "" {
		return nil
	}
	c, err := wucache.New(winCacheDir(dir), "patchd-server/"+version, logger)
	if err != nil {
		logger.Error("cache do Windows Update desligado", "error", err)
		return nil
	}
	return c
}

// winCachePruneEvery: de quanto em quanto tempo a limpeza passa.
const winCachePruneEvery = time.Hour

// pruneWinCacheLoop apaga, de hora em hora, o que nenhuma máquina pediu no prazo do parâmetro.
func pruneWinCacheLoop(ctx context.Context, logger *slog.Logger, c *wucache.Cache, params *settings.Service) {
	t := time.NewTicker(winCachePruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		days := defaultWinCacheDays
		if params != nil {
			if n, err := strconv.Atoi(params.Value(api.WinCacheDaysKey)); err == nil && n > 0 {
				days = n
			}
		}
		n, bytes, err := c.Prune(time.Now().Add(-time.Duration(days) * 24 * time.Hour))
		switch {
		case err != nil:
			logger.Error("limpeza do cache do Windows Update falhou", "error", err)
		case n > 0:
			logger.Info("limpeza do cache do Windows Update", "removidos", n, "removed_mib", mib(bytes), "dias", days)
		}
	}
}
