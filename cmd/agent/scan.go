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
	// A busca online no WSUS pode levar minutos: prazo próprio, separado do inventário.
	scanTimeout = 10 * time.Minute
	// Entradas mais recentes do histórico incluídas no relatório.
	historyMax = 100
)

// collectScan busca faltantes e histórico. Cada seção falha isoladamente.
func collectScan(ctx context.Context, s patch.Scanner, agentVersion string) protocol.PatchScanReport {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	report := protocol.PatchScanReport{
		SchemaVersion: protocol.PatchSchemaVersion,
		AgentVersion:  agentVersion,
		ScannedAt:     time.Now().UTC(),
	}

	missing, err := s.Missing(ctx)
	report.AddErrors("missing", err)
	if err == nil && missing == nil {
		missing = []protocol.MissingUpdate{}
	}
	report.Missing = missing

	history, err := s.History(ctx, historyMax)
	report.AddErrors("history", err)
	if err == nil && history == nil {
		history = []protocol.UpdateHistoryEntry{}
	}
	report.History = history

	return report
}

// printScan executa a busca e escreve o relatório em JSON indentado em w.
func printScan(ctx context.Context, w io.Writer, s patch.Scanner, agentVersion string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(collectScan(ctx, s, agentVersion))
}
