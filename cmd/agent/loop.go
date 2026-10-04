package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/patch"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// loopOptions é o que o laço de check-in precisa da configuração.
type loopOptions struct {
	DataDir        string
	ServerURL      string // vazio: usa o servidor gravado na credencial
	OfflineCatalog string
	Interval       time.Duration
	AllowContainer bool // PATCHD_ALLOW_CONTAINER=1: só para desenvolvimento
	// AutoUpdate: só como serviço (Aula 5.4). ServiceArgs são as opções do "service run",
	// repassadas ao install da versão nova para o serviço continuar com as mesmas opções.
	AutoUpdate  bool
	ServiceArgs []string
}

// inContainer é a detecção de container; os testes a substituem.
var inContainer = platform.InContainer

// allowContainer lê a exceção de desenvolvimento PATCHD_ALLOW_CONTAINER=1.
func allowContainer(look config.Lookup) bool {
	v, ok := look("PATCHD_ALLOW_CONTAINER")
	return ok && v == "1"
}

// containerCheck diz se o agente está num container (why) e se deve recusar (refuse).
// Dentro de um container, os arquivos são os da imagem e o kernel é o do host (Aula 2.1):
// o inventário, a busca e o reinício pendente descreveriam uma máquina que não existe, e
// o enrollment criaria no servidor uma "máquina" que é só um container.
func containerCheck(allow bool) (why string, refuse bool) {
	in, why := inContainer()
	if !in {
		return "", false
	}
	return why, !allow
}

// runLoop roda o laço de check-in até o contexto acabar. É o mesmo laço no terminal
// (Ctrl+C) e como serviço (parada pedida pelo gerenciador de serviços).
func runLoop(ctx context.Context, logger *slog.Logger, opts loopOptions) int {
	info := buildinfo.Get()

	if why, refuse := containerCheck(opts.AllowContainer); refuse {
		logger.Error("o agente não roda dentro de container: inventário, busca e reinício pendente descreveriam a imagem e o host",
			"evidence", why)
		return exitConfig
	} else if why != "" {
		logger.Warn("rodando dentro de container por PATCHD_ALLOW_CONTAINER=1 (só para desenvolvimento)", "evidence", why)
	}

	cred, err := credential.Load(opts.DataDir)
	if errors.Is(err, credential.ErrNotEnrolled) {
		logger.Error("máquina não registrada: execute \"patchd-agent enroll\" antes", "data_dir", opts.DataDir)
		return exitRuntime
	}
	if err != nil {
		logger.Error("credencial ilegível", "error", err)
		return exitRuntime
	}
	// A credencial só vale no servidor que a emitiu.
	if opts.ServerURL != "" && strings.TrimRight(opts.ServerURL, "/") != strings.TrimRight(cred.ServerURL, "/") {
		logger.Error("o servidor configurado não é o que emitiu a credencial",
			"configured", opts.ServerURL, "credential_server", cred.ServerURL)
		return exitConfig
	}

	collector := inventory.New(platform.ExecRunner{})
	scanner := patch.New(platform.ExecRunner{Timeout: scanCommandTimeout})
	scanOpts := patch.Options{OfflineCatalog: opts.OfflineCatalog}

	client := agent.NewClient(cred.ServerURL, cred.Credential, info.Version)
	ag := &agent.Agent{
		Client: client,
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
			return collectScan(ctx, scanner, scanOpts, info.Version)
		},
		DataDir:   opts.DataDir,
		Interval:  opts.Interval,
		ScanEvery: scanEvery,
		Logger:    logger,
	}

	if opts.AutoUpdate {
		ag.Update = newUpdater(logger, client, opts, info.Version)
	}

	logger.Info("agente iniciado",
		"commit", info.Commit,
		"os", info.OS,
		"arch", info.Arch,
		"platform", platform.Name,
		"machine_id", cred.MachineID,
		"server", cred.ServerURL,
		"data_dir", opts.DataDir,
	)
	if err := ag.Run(ctx); err != nil {
		logger.Error("laço de check-in parou com erro", "error", err)
		return exitRuntime
	}
	logger.Info("agente encerrado")
	return exitOK
}
