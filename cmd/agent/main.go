// Comando patchd-agent: agente que coleta inventário, escaneia patches e executa jobs.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/patch"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Códigos de saída do agente.
const (
	exitOK      = 0
	exitRuntime = 1 // falha durante a execução (coleta, rede etc.)
	exitConfig  = 2 // mesma convenção do pacote flag para erro de uso
)

const (
	// scanCommandTimeout é o prazo de cada comando de busca (o dnf pode baixar metadados).
	scanCommandTimeout = 15 * time.Minute
	// scanEvery é o período da busca de atualizações (RNF-03: uma vez por dia).
	scanEvery = 24 * time.Hour
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run concentra a lógica do agente. Argumentos, ambiente e saídas chegam como
// parâmetros para que a função possa ser testada sem criar um processo.
func run(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	info := buildinfo.Get()

	// Subcomandos: "patchd-agent identity" e "patchd-agent enroll [opções]".
	if len(args) > 0 {
		switch args[0] {
		case "identity":
			return runIdentity(args[1:], stdout, stderr)
		case "enroll":
			return runEnroll(args[1:], look, stdout, stderr, info.Version)
		}
	}

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

	collector := inventory.New(platform.ExecRunner{})
	scanner := patch.New(platform.ExecRunner{Timeout: scanCommandTimeout})
	opts := patch.Options{OfflineCatalog: cfg.OfflineCatalog}

	if cfg.Inventory {
		// JSON limpo em stdout; eventuais logs continuam em stderr.
		if err := printInventory(context.Background(), stdout, collector, info.Version); err != nil {
			logger.Error("falha na coleta do inventário", "error", err)
			return exitRuntime
		}
		return exitOK
	}
	if cfg.Scan {
		if err := printScan(context.Background(), stdout, scanner, opts, info.Version); err != nil {
			logger.Error("falha na busca de atualizações", "error", err)
			return exitRuntime
		}
		return exitOK
	}

	// Sem -inventory nem -scan: o agente roda o laço de check-in até ser encerrado.
	cred, err := credential.Load(cfg.DataDir)
	if errors.Is(err, credential.ErrNotEnrolled) {
		logger.Error("máquina não registrada: execute \"patchd-agent enroll\" antes", "data_dir", cfg.DataDir)
		return exitRuntime
	}
	if err != nil {
		logger.Error("credencial ilegível", "error", err)
		return exitRuntime
	}
	// A credencial só vale no servidor que a emitiu.
	if cfg.ServerURL != "" && strings.TrimRight(cfg.ServerURL, "/") != strings.TrimRight(cred.ServerURL, "/") {
		logger.Error("o servidor configurado não é o que emitiu a credencial",
			"configured", cfg.ServerURL, "credential_server", cred.ServerURL)
		return exitConfig
	}

	// SIGTERM vem do gerenciador de serviços (systemd, launchd); SIGINT, do Ctrl+C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ag := &agent.Agent{
		Client: agent.NewClient(cred.ServerURL, cred.Credential, info.Version),
		// O inventário passa pela mesma função do "-inventory": o check-in envia exatamente
		// o JSON que você conferiu nas aulas do Módulo 2.
		Inventory: func(ctx context.Context) (protocol.InventoryReport, error) {
			var buf bytes.Buffer
			if err := printInventory(ctx, &buf, collector, info.Version); err != nil {
				return protocol.InventoryReport{}, err
			}
			var rep protocol.InventoryReport
			err := json.Unmarshal(buf.Bytes(), &rep)
			return rep, err
		},
		Scan: func(ctx context.Context) protocol.PatchScanReport {
			return collectScan(ctx, scanner, opts, info.Version)
		},
		DataDir:   cfg.DataDir,
		Interval:  cfg.CheckinInterval,
		ScanEvery: scanEvery,
		Logger:    logger,
	}

	logger.Info("agente iniciado",
		"commit", info.Commit,
		"os", info.OS,
		"arch", info.Arch,
		"platform", platform.Name,
		"machine_id", cred.MachineID,
		"server", cred.ServerURL,
		"data_dir", cfg.DataDir,
	)
	if err := ag.Run(ctx); err != nil {
		logger.Error("laço de check-in parou com erro", "error", err)
		return exitRuntime
	}
	logger.Info("agente encerrado")
	return exitOK
}
