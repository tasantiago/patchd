package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/patch"
	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	// Busca online no WSUS e varredura offline do catálogo podem levar vários minutos cada.
	scanTimeout = 20 * time.Minute
	// Entradas mais recentes do histórico incluídas no relatório.
	historyMax = 100
)

// collectScan executa as buscas e lê o histórico. Cada busca traz o próprio erro;
// falhas do histórico vão para Errors.
func collectScan(ctx context.Context, s patch.Scanner, opts patch.Options, agentVersion string) protocol.PatchScanReport {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	report := protocol.PatchScanReport{
		SchemaVersion: protocol.PatchSchemaVersion,
		AgentVersion:  agentVersion,
		ScannedAt:     time.Now().UTC(),
		Scans:         s.Scan(ctx, opts),
	}

	history, err := s.History(ctx, historyMax)
	report.AddErrors("history", err)
	if err == nil && history == nil {
		history = []protocol.UpdateHistoryEntry{}
	}
	report.History = history
	return report
}

// printScan executa a busca e escreve o relatório em JSON indentado em w.
func printScan(ctx context.Context, w io.Writer, s patch.Scanner, opts patch.Options, agentVersion string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(collectScan(ctx, s, opts, agentVersion))
}
