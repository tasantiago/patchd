package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	// Hardware: null = não coletado (motivo em Errors); objeto = coletado, talvez parcialmente.
	Hardware *inventory.Hardware `json:"hardware"`
	// Software: null = não coletado (motivo em Errors); [] = coletado, nenhum item.
	// Em falha parcial, traz o que foi lido e o erro aparece em Errors.
	Software []inventory.Software `json:"software"`
	// Errors lista as falhas, uma por entrada, com o prefixo da seção. Uma falha
	// nunca fica escondida atrás de uma lista vazia.
	Errors []string `json:"errors,omitempty"`
}

// addErrors acrescenta cada linha de err como uma entrada própria em Errors, com o
// prefixo da seção. errors.Join junta mensagens com quebra de linha; aqui elas se separam.
func (r *inventoryReport) addErrors(section string, err error) {
	if err == nil {
		return
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r.Errors = append(r.Errors, section+": "+line)
		}
	}
}

// printInventory coleta com c e escreve o relatório em JSON indentado em w.
// A identificação do SO é obrigatória; as demais seções falham isoladamente.
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

	hw, err := c.Hardware(ctx)
	report.addErrors("hardware", err)
	if !errors.Is(err, inventory.ErrNotImplemented) {
		// Coletado, inteiro ou em parte.
		report.Hardware = &hw
	}

	software, err := c.Software(ctx)
	report.addErrors("software", err)
	if err == nil && software == nil {
		// Coleta bem-sucedida sem itens: [] no JSON, e não null.
		software = []inventory.Software{}
	}
	report.Software = software

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Sem escapar <, > e &: a saída é para leitura e para o servidor, não para HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(report)
}
