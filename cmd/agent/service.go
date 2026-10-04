package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
)

// Nome do serviço e limites do log em arquivo.
const (
	serviceName    = "patchd-agent"
	logFileMaxSize = 10 << 20 // 10 MB por arquivo
	logFileKeep    = 3        // agent.log, .1, .2, .3: até 40 MB de histórico
)

const serviceUsage = `uso: patchd-agent service <comando> [opções do agente]

comandos:
  install     instala e inicia o serviço (exige administrador/root; a máquina já registrada)
  uninstall   para e remove o serviço (a pasta de dados e a credencial ficam)
  run         o que o gerenciador de serviços executa; no terminal, roda em primeiro plano
`

// runService despacha "patchd-agent service ...".
func runService(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, serviceUsage)
		return exitConfig
	}
	switch args[0] {
	case "install":
		return serviceInstall(args[1:], look, stdout, stderr)
	case "uninstall":
		return serviceUninstall(stdout, stderr)
	case "run":
		return serviceRun(args[1:], look, stderr)
	default:
		fmt.Fprintf(stderr, "patchd-agent: comando de serviço desconhecido %q\n\n%s", args[0], serviceUsage)
		return exitConfig
	}
}

// serviceRun roda o laço. Sob o gerenciador de serviços, protege a pasta de dados e usa o
// log do sistema escolhido para ele (serviceLog); no terminal, usa o stderr.
func serviceRun(args []string, look config.Lookup, stderr io.Writer) int {
	cfg, err := loadAgentConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida:\n%v\n", err)
		return exitConfig
	}

	asService := isServiceProcess()
	if asService {
		if err := protectDataDir(cfg.DataDir); err != nil {
			// Ainda não há log próprio: vai para o stderr que o gerenciador guardar (se guardar).
			fmt.Fprintf(stderr, "patchd-agent: proteger %s: %v\n", cfg.DataDir, err)
			return exitRuntime
		}
	}
	out, closeLog, err := serviceLog(cfg.DataDir, asService, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: abrir o log: %v\n", err)
		return exitRuntime
	}
	defer closeLog()

	logger, err := logging.New(out, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitConfig
	}
	logger = logger.With("app", "patchd-agent", "version", buildinfo.Get().Version)

	opts := loopOptions{
		DataDir:        cfg.DataDir,
		ServerURL:      cfg.ServerURL,
		OfflineCatalog: cfg.OfflineCatalog,
		Interval:       cfg.CheckinInterval,
		AllowContainer: allowContainer(look),
		AutoUpdate:     asService,
		ServiceArgs:    args,
	}
	if asService {
		return runAsService(logger, func(ctx context.Context) int { return runLoop(ctx, logger, opts) })
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runLoop(ctx, logger, opts)
}

// rotatingLog abre o log rotativo na pasta de dados (Windows e macOS, que não têm um
// journal que o serviço use por padrão).
func rotatingLog(dataDir string) (io.Writer, func(), error) {
	rf, err := logging.OpenRotating(filepath.Join(dataDir, "logs", "agent.log"), logFileMaxSize, logFileKeep)
	if err != nil {
		return nil, nil, err
	}
	return rf, func() { rf.Close() }, nil
}
