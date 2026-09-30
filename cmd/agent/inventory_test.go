package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/inventory"
)

// coletorFalso cumpre inventory.Collector só por ter os métodos OS e Software.
type coletorFalso struct {
	info        inventory.OSInfo
	osErr       error
	software    []inventory.Software
	softwareErr error
}

func (c coletorFalso) OS(ctx context.Context) (inventory.OSInfo, error) { return c.info, c.osErr }

func (c coletorFalso) Software(ctx context.Context) ([]inventory.Software, error) {
	return c.software, c.softwareErr
}

// imprime executa printInventory e devolve o JSON cru e o relatório decodificado.
func imprime(t *testing.T, c coletorFalso) (string, inventoryReport) {
	t.Helper()
	var buf bytes.Buffer
	if err := printInventory(context.Background(), &buf, c, "v0.2.0-teste"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	var rep inventoryReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("saída não é JSON válido: %v\n%s", err, buf.String())
	}
	return buf.String(), rep
}

var linuxFalso = inventory.OSInfo{Family: "linux", ID: "ubuntu", Version: "26.04"}

func TestPrintInventoryBasico(t *testing.T) {
	_, rep := imprime(t, coletorFalso{info: linuxFalso})
	if rep.AgentVersion != "v0.2.0-teste" || rep.OS.ID != "ubuntu" || rep.OS.Version != "26.04" {
		t.Errorf("relatório errado: %+v", rep)
	}
	if rep.CollectedAt.IsZero() || rep.CollectedAt.Location().String() != "UTC" {
		t.Errorf("collected_at deveria ser preenchido em UTC: %v", rep.CollectedAt)
	}
}

func TestPrintInventorySoftwareVazioEhListaVazia(t *testing.T) {
	cru, rep := imprime(t, coletorFalso{info: linuxFalso})
	if !strings.Contains(cru, `"software": []`) {
		t.Errorf("coleta sem itens deveria gerar \"software\": [], veio:\n%s", cru)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("sem falhas, errors deveria estar vazio: %v", rep.Errors)
	}
}

func TestPrintInventorySoftwareNaoImplementadoEhNull(t *testing.T) {
	cru, rep := imprime(t, coletorFalso{info: linuxFalso, softwareErr: inventory.ErrNotImplemented})
	if !strings.Contains(cru, `"software": null`) {
		t.Errorf("seção não coletada deveria gerar \"software\": null, veio:\n%s", cru)
	}
	if len(rep.Errors) != 1 || !strings.HasPrefix(rep.Errors[0], "software: ") {
		t.Errorf("errors deveria explicar a falha do software: %v", rep.Errors)
	}
}

func TestPrintInventorySoftwareParcial(t *testing.T) {
	c := coletorFalso{
		info:        linuxFalso,
		software:    []inventory.Software{{Name: "Produto A", Scope: "machine", Source: "x"}},
		softwareErr: errors.New("HKU\\S-1-5-21-1: acesso negado"),
	}
	_, rep := imprime(t, c)
	if len(rep.Software) != 1 || rep.Software[0].Name != "Produto A" {
		t.Errorf("em falha parcial, o que foi lido deveria vir no relatório: %+v", rep.Software)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "acesso negado") {
		t.Errorf("em falha parcial, o erro deveria vir em errors: %v", rep.Errors)
	}
}

func TestPrintInventoryFalhaNoSO(t *testing.T) {
	var buf bytes.Buffer
	c := coletorFalso{osErr: errors.New("sem os-release")}
	if err := printInventory(context.Background(), &buf, c, "v"); err == nil {
		t.Error("esperado erro quando a identificação do SO falha")
	}
	if buf.Len() != 0 {
		t.Errorf("nada deveria ser impresso em caso de erro: %q", buf.String())
	}
}
