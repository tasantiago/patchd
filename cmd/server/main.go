// Comando patchd-server: API, catálogo, motor de compliance e fila de jobs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
)

// Códigos de saída do servidor.
const (
	exitOK     = 0
	exitConfig = 2 // mesma convenção do pacote flag para erro de uso
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run concentra a lógica do servidor. Argumentos, ambiente e saídas chegam como
// parâmetros para que a função possa ser testada sem criar um processo.
func run(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	info := buildinfo.Get()

	cfg, err := loadServerConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "patchd-server %s\n", info)
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

	logger.Info("servidor iniciado",
		"commit", info.Commit,
		"listen", cfg.ListenAddr,
		"database", redactDatabaseURL(cfg.DatabaseURL),
	)
	if cfg.DatabaseURL == "" {
		logger.Warn("PATCHD_DATABASE_URL não definido: o banco será obrigatório a partir do Módulo 4")
	}
	logger.Info("servidor encerrado: ainda sem funcionalidades (Aula 1.3)")
	return exitOK
}
