// Comando patchd-agent: agente que coleta inventário, escaneia patches e executa jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/patch"
	"github.com/tasantiago/patchd/internal/platform"
)

// Códigos de saída do agente.
const (
	exitOK      = 0
	exitRuntime = 1 // falha durante a execução (coleta, rede etc.)
	exitConfig  = 2 // mesma convenção do pacote flag para erro de uso
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run concentra a lógica do agente. Argumentos, ambiente e saídas chegam como
// parâmetros para que a função possa ser testada sem criar um processo.
func run(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	info := buildinfo.Get()

	cfg, err := loadAgentConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		// O pacote flag já mostrou a mensagem e a ajuda; não repetir.
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "patchd-agent %s\n", info)
		return exitOK
	}

	logger, err := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		// Não deveria acontecer: nível e formato já foram validados.
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitConfig
	}
	// Campos presentes em todas as linhas de log do agente.
	logger = logger.With("app", "patchd-agent", "version", info.Version)
	slog.SetDefault(logger)

	runner := platform.ExecRunner{}

	if cfg.Inventory {
		// JSON limpo em stdout; eventuais logs continuam em stderr.
		if err := printInventory(context.Background(), stdout, inventory.New(runner), info.Version); err != nil {
			logger.Error("falha na coleta do inventário", "error", err)
			return exitRuntime
		}
		return exitOK
	}

	if cfg.Scan {
		opts := patch.Options{OfflineCatalog: cfg.OfflineCatalog}
		if err := printScan(context.Background(), stdout, patch.New(runner), opts, info.Version); err != nil {
			logger.Error("falha na busca de atualizações", "error", err)
			return exitRuntime
		}
		return exitOK
	}

	logger.Info("agente iniciado",
		"commit", info.Commit,
		"os", info.OS,
		"arch", info.Arch,
		"platform", platform.Name,
		"data_dir", cfg.DataDir,
		"server", cfg.ServerURL,
		"checkin_interval", cfg.CheckinInterval.String(),
	)
	logger.Debug("configuração de log", "log_level", cfg.LogLevel, "log_format", cfg.LogFormat)

	if cfg.ServerURL == "" {
		logger.Warn("PATCHD_SERVER_URL não definido: o agente não fará check-in")
	}
	logger.Info("agente encerrado: sem serviço ainda; use -inventory ou -scan")
	return exitOK
}
