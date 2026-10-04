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
}

// runLoop roda o laço de check-in até o contexto acabar. É o mesmo laço no terminal
// (Ctrl+C) e como serviço (parada pedida pelo gerenciador de serviços).
func runLoop(ctx context.Context, logger *slog.Logger, opts loopOptions) int {
	info := buildinfo.Get()

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
			return collectScan(ctx, scanner, scanOpts, info.Version)
		},
		DataDir:   opts.DataDir,
		Interval:  opts.Interval,
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
		"data_dir", opts.DataDir,
	)
	if err := ag.Run(ctx); err != nil {
		logger.Error("laço de check-in parou com erro", "error", err)
		return exitRuntime
	}
	logger.Info("agente encerrado")
	return exitOK
}
