package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/protocol"
)

// coletorFalso cumpre inventory.Collector só por ter os métodos OS, Software e Hardware.
type coletorFalso struct {
	info        inventory.OSInfo
	osErr       error
	software    []inventory.Software
	softwareErr error
	hardware    inventory.Hardware
	hardwareErr error
}

func (c coletorFalso) OS(ctx context.Context) (inventory.OSInfo, error) { return c.info, c.osErr }

func (c coletorFalso) Software(ctx context.Context) ([]inventory.Software, error) {
	return c.software, c.softwareErr
}

func (c coletorFalso) Hardware(ctx context.Context) (inventory.Hardware, error) {
	return c.hardware, c.hardwareErr
}

// imprime executa printInventory e devolve o JSON cru e o relatório decodificado.
func imprime(t *testing.T, c coletorFalso) (string, protocol.InventoryReport) {
	t.Helper()
	var buf bytes.Buffer
	if err := printInventory(context.Background(), &buf, c, "v0.2.0-teste"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	var rep protocol.InventoryReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("saída não é JSON válido: %v\n%s", err, buf.String())
	}
	return buf.String(), rep
}

var linuxFalso = inventory.OSInfo{Family: "linux", ID: "ubuntu", Version: "26.04"}

func TestPrintInventoryBasico(t *testing.T) {
	_, rep := imprime(t, coletorFalso{info: linuxFalso})
	if rep.SchemaVersion != protocol.InventorySchemaVersion || rep.AgentVersion != "v0.2.0-teste" || rep.OS.ID != "ubuntu" {
		t.Errorf("relatório errado: %+v", rep)
	}
	if rep.CollectedAt.IsZero() || rep.CollectedAt.Location().String() != "UTC" {
		t.Errorf("collected_at deveria ser preenchido em UTC: %v", rep.CollectedAt)
	}
	if !strings.HasPrefix(rep.Hash, "sha256:") {
		t.Errorf("o relatório deveria trazer o hash do conteúdo: %q", rep.Hash)
	}
}

func TestPrintInventoryHashEstavelEntreColetas(t *testing.T) {
	c := coletorFalso{
		info:     linuxFalso,
		software: []inventory.Software{{Name: "b", Source: "x"}, {Name: "a", Source: "x"}},
	}
	_, r1 := imprime(t, c)
	c.software = []inventory.Software{{Name: "a", Source: "x"}, {Name: "b", Source: "x"}}
	_, r2 := imprime(t, c)
	if r1.Hash != r2.Hash {
		t.Errorf("mesmo conteúdo em outra ordem e em outro horário deveria dar o mesmo hash: %s != %s", r1.Hash, r2.Hash)
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

func TestPrintInventoryNaoImplementadoEhNull(t *testing.T) {
	c := coletorFalso{info: linuxFalso, softwareErr: inventory.ErrNotImplemented, hardwareErr: inventory.ErrNotImplemented}
	cru, rep := imprime(t, c)
	if !strings.Contains(cru, `"software": null`) || !strings.Contains(cru, `"hardware": null`) {
		t.Errorf("seções não coletadas deveriam ser null, veio:\n%s", cru)
	}
	if len(rep.Errors) != 2 {
		t.Errorf("errors deveria explicar as duas seções: %v", rep.Errors)
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

func TestPrintInventoryHardwareParcialUmErroPorEntrada(t *testing.T) {
	c := coletorFalso{
		info:     linuxFalso,
		hardware: inventory.Hardware{Manufacturer: "LENOVO"},
		hardwareErr: errors.Join(
			errors.New("/sys/class/dmi/id/product_serial: permissão negada (exige root)"),
			errors.New("/sys/class/dmi/id/product_uuid: permissão negada (exige root)"),
		),
	}
	_, rep := imprime(t, c)
	if rep.Hardware == nil || rep.Hardware.Manufacturer != "LENOVO" {
		t.Errorf("hardware parcial deveria vir no relatório: %+v", rep.Hardware)
	}
	if len(rep.Errors) != 2 {
		t.Fatalf("esperadas 2 entradas em errors, uma por falha: %q", rep.Errors)
	}
	for _, e := range rep.Errors {
		if !strings.HasPrefix(e, "hardware: ") || strings.Contains(e, "\n") {
			t.Errorf("cada entrada precisa do prefixo da seção e nenhuma quebra de linha: %q", e)
		}
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
