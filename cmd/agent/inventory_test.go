package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tasantiago/patchd/internal/inventory"
)

// coletorFalso cumpre inventory.Collector só por ter o método OS.
type coletorFalso struct {
	info inventory.OSInfo
	err  error
}

func (c coletorFalso) OS(ctx context.Context) (inventory.OSInfo, error) { return c.info, c.err }

func TestPrintInventory(t *testing.T) {
	var buf bytes.Buffer
	c := coletorFalso{info: inventory.OSInfo{Family: "linux", ID: "ubuntu", Version: "26.04"}}
	if err := printInventory(context.Background(), &buf, c, "v0.1.0-teste"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	var got inventoryReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("saída não é JSON válido: %v\n%s", err, buf.String())
	}
	if got.AgentVersion != "v0.1.0-teste" || got.OS.ID != "ubuntu" || got.OS.Version != "26.04" {
		t.Errorf("relatório errado: %+v", got)
	}
	if got.CollectedAt.IsZero() || got.CollectedAt.Location().String() != "UTC" {
		t.Errorf("collected_at deveria ser preenchido em UTC: %v", got.CollectedAt)
	}
}

func TestPrintInventoryPropagaErro(t *testing.T) {
	var buf bytes.Buffer
	c := coletorFalso{err: errors.New("sem os-release")}
	if err := printInventory(context.Background(), &buf, c, "v"); err == nil {
		t.Error("esperado erro do coletor")
	}
	if buf.Len() != 0 {
		t.Errorf("nada deveria ser impresso em caso de erro: %q", buf.String())
	}
}
