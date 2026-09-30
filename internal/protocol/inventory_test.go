package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func relatorioBase() InventoryReport {
	return InventoryReport{
		SchemaVersion: InventorySchemaVersion,
		AgentVersion:  "v0.2.0",
		CollectedAt:   time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		OS:            OSInfo{Family: "linux", ID: "ubuntu", Version: "26.04", Hostname: "itom-teste"},
		Hardware:      &Hardware{Manufacturer: "VMware, Inc.", MemoryBytes: 3513413632},
		Software: []Software{
			{Name: "libcares2", Version: "1.34.6-1ubuntu0.1", Scope: "machine", Source: "dpkg"},
			{Name: "bind9-host", Version: "1:9.20.24-1ubuntu0.3", Scope: "machine", Source: "dpkg"},
		},
	}
}

func hash(t *testing.T, r InventoryReport) string {
	t.Helper()
	h, err := r.ContentHash()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.HasPrefix(h, "sha256:") || len(h) != len("sha256:")+64 {
		t.Fatalf("formato de hash inesperado: %q", h)
	}
	return h
}

func TestHashIgnoraOrdemDoSoftware(t *testing.T) {
	a := relatorioBase()
	b := relatorioBase()
	b.Software[0], b.Software[1] = b.Software[1], b.Software[0]
	if hash(t, a) != hash(t, b) {
		t.Error("a mesma lista em outra ordem deveria dar o mesmo hash")
	}
	if a.Software[0].Name != "libcares2" {
		t.Error("o hash não pode reordenar a lista original do relatório")
	}
}

func TestHashIgnoraCamposVolateis(t *testing.T) {
	a := relatorioBase()
	b := relatorioBase()
	b.CollectedAt = b.CollectedAt.Add(time.Hour)
	b.AgentVersion = "v0.3.0"
	b.AddErrors("hardware", errors.New("product_serial: permissão negada"))
	if hash(t, a) != hash(t, b) {
		t.Error("collected_at, agent_version e errors não podem mudar o hash")
	}
}

func TestHashMudaComOConteudo(t *testing.T) {
	base := hash(t, relatorioBase())

	mudancas := map[string]func(*InventoryReport){
		"versão de pacote": func(r *InventoryReport) { r.Software[0].Version = "1.34.6-1" },
		"pacote travado":   func(r *InventoryReport) { r.Software[0].Held = true },
		"hostname":         func(r *InventoryReport) { r.OS.Hostname = "outro" },
		"memória":          func(r *InventoryReport) { r.Hardware.MemoryBytes = 1 },
		"versão do schema": func(r *InventoryReport) { r.SchemaVersion = 2 },
	}
	for nome, muda := range mudancas {
		t.Run(nome, func(t *testing.T) {
			r := relatorioBase()
			muda(&r)
			if hash(t, r) == base {
				t.Errorf("mudar %s deveria mudar o hash", nome)
			}
		})
	}
}

func TestHashDistingueNullDeListaVazia(t *testing.T) {
	a := relatorioBase()
	a.Software = nil
	b := relatorioBase()
	b.Software = []Software{}
	if hash(t, a) == hash(t, b) {
		t.Error("\"não coletado\" (null) e \"nenhum item\" ([]) precisam ter hashes diferentes")
	}
}

func TestAddErrorsUmaEntradaPorLinha(t *testing.T) {
	var r InventoryReport
	r.AddErrors("hardware", errors.Join(errors.New("serial: negado"), errors.New("uuid: negado")))
	r.AddErrors("software", nil)
	if len(r.Errors) != 2 || r.Errors[0] != "hardware: serial: negado" || r.Errors[1] != "hardware: uuid: negado" {
		t.Errorf("errors inesperado: %q", r.Errors)
	}
}
