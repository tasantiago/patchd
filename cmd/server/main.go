// Comando patchd-server: API, catálogo, motor de compliance e fila de jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/store"
)

// Códigos de saída do servidor.
const (
	exitOK      = 0
	exitRuntime = 1 // falha durante a execução (porta ocupada, banco indisponível etc.)
	exitConfig  = 2 // mesma convenção do pacote flag para erro de uso
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run concentra a lógica do servidor. Argumentos, ambiente e saídas chegam como
// parâmetros para que a função possa ser testada sem criar um processo.
func run(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	info := buildinfo.Get()

	// Subcomandos de administração: "patchd-server token create|list|revoke",
	// "patchd-server release current|publish|withdraw", "patchd-server catalog sync|list|builds|usn|fedora|apple|kev"
	// e "patchd-server compliance -machine ID".
	if len(args) > 0 && args[0] == "token" {
		return runToken(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "release" {
		return runRelease(args[1:], look, stdout, stderr, info.Version)
	}
	if len(args) > 0 && args[0] == "catalog" {
		return runCatalog(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "compliance" {
		return runCompliance(args[1:], look, stdout, stderr)
	}

	cfg, err := loadServerConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		// O pacote flag já mostrou a mensagem e a ajuda; não repetir.
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "patchd-server %s\n", info)
		return exitOK
	}
	if cfg.HealthCheck {
		if err := healthcheck(cfg.ListenAddr); err != nil {
			fmt.Fprintf(stderr, "patchd-server: healthcheck falhou: %v\n", err)
			return exitRuntime
		}
		return exitOK
	}

	logger, err := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		// Não deveria acontecer: nível e formato já foram validados.
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	logger = logger.With("app", "patchd-server", "version", info.Version)
	slog.SetDefault(logger)

	// SIGTERM vem do "docker stop"; SIGINT, do Ctrl+C no terminal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("servidor iniciado",
		"commit", info.Commit,
		"listen", cfg.ListenAddr,
		"database", redactDatabaseURL(cfg.DatabaseURL),
	)
	logger.Warn("API sem autenticação: mantenha a porta restrita a 127.0.0.1 (enrollment na 4.3, TLS na 9.1)")

	// Banco primeiro: conecta, aplica as migrations e só então a porta é aberta.
	st, closeStore, err := openStore(ctx, logger, cfg.DatabaseURL)
	if err != nil {
		logger.Error("armazenamento indisponível", "error", err)
		return exitRuntime
	}
	defer closeStore()
	if p, ok := st.(scanPruner); ok {
		go pruneScansLoop(ctx, logger, p)
	}
	switch pg, ok := st.(*store.Postgres); {
	case !ok:
		// Sem banco não há catálogo.
	case cfg.CatalogInterval == 0:
		logger.Info("sincronização do catálogo desligada (PATCHD_CATALOG_INTERVAL=0): use patchd-server catalog sync")
	default:
		go catalogLoop(ctx, logger, pg, newCatalogSources(look), cfg.CatalogInterval, firstCatalogRun())
	}

	var apiOpts []api.Option
	if cfg.ReleasesDir != "" {
		apiOpts = append(apiOpts, api.WithReleases(release.Dir(cfg.ReleasesDir), info.Version))
		logReleases(logger, release.Dir(cfg.ReleasesDir), info.Version)
	}
	handler := newHandler(api.New(st, logger, apiOpts...))
	if err := serve(ctx, logger, cfg.ListenAddr, handler); err != nil {
		logger.Error("servidor parou com erro", "error", err)
		return exitRuntime
	}
	logger.Info("servidor encerrado")
	return exitOK
}
