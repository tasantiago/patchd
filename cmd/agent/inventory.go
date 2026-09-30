package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/protocol"
)

// inventoryTimeout é o prazo total da coleta disparada por -inventory.
const inventoryTimeout = 2 * time.Minute

// collectInventory coleta todas as seções e monta o relatório do protocolo, com o hash
// do conteúdo. A identificação do SO é obrigatória; as demais seções falham isoladamente.
func collectInventory(ctx context.Context, c inventory.Collector, agentVersion string) (protocol.InventoryReport, error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()

	osInfo, err := c.OS(ctx)
	if err != nil {
		return protocol.InventoryReport{}, fmt.Errorf("identificação do SO: %w", err)
	}
	report := protocol.InventoryReport{
		SchemaVersion: protocol.InventorySchemaVersion,
		AgentVersion:  agentVersion,
		CollectedAt:   time.Now().UTC(),
		OS:            osInfo,
	}

	hw, err := c.Hardware(ctx)
	report.AddErrors("hardware", err)
	if !errors.Is(err, inventory.ErrNotImplemented) {
		// Coletado, inteiro ou em parte.
		report.Hardware = &hw
	}

	software, err := c.Software(ctx)
	report.AddErrors("software", err)
	if err == nil && software == nil {
		// Coleta bem-sucedida sem itens: [] no JSON, e não null.
		software = []inventory.Software{}
	}
	report.Software = software

	report.Hash, err = report.ContentHash()
	if err != nil {
		return protocol.InventoryReport{}, err
	}
	return report, nil
}

// printInventory coleta com c e escreve o relatório em JSON indentado em w.
func printInventory(ctx context.Context, w io.Writer, c inventory.Collector, agentVersion string) error {
	report, err := collectInventory(ctx, c, agentVersion)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Sem escapar <, > e &: a saída é para leitura e para o servidor, não para HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(report)
}
