package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/inventory"
)

// inventoryTimeout é o prazo total da coleta disparada por -inventory.
const inventoryTimeout = 2 * time.Minute

// inventoryReport é o que o agente imprime com -inventory. Cresce ao longo do Módulo 2.
type inventoryReport struct {
	AgentVersion string           `json:"agent_version"`
	CollectedAt  time.Time        `json:"collected_at"` // sempre em UTC
	OS           inventory.OSInfo `json:"os"`
}

// printInventory coleta com c e escreve o relatório em JSON indentado em w.
func printInventory(ctx context.Context, w io.Writer, c inventory.Collector, agentVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()

	osInfo, err := c.OS(ctx)
	if err != nil {
		return fmt.Errorf("identificação do SO: %w", err)
	}

	report := inventoryReport{
		AgentVersion: agentVersion,
		CollectedAt:  time.Now().UTC(),
		OS:           osInfo,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Sem escapar <, > e &: a saída é para leitura e para o servidor, não para HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(report)
}
