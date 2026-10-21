package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// TestFirmwareNaFrota (Aula 21.2): os cartões contam a frota, filtram, e a coluna e o CSV levam
// o firmware.
func TestFirmwareNaFrota(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	salva := func(id, hash string, fw *protocol.Firmware) {
		t.Helper()
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: hash,
			OS: protocol.OSInfo{Family: "windows", Hostname: id[:8]}, Hardware: &protocol.Hardware{Manufacturer: "HP", Sources: []string{}, Firmware: fw}}); err != nil {
			t.Fatal(err)
		}
	}
	salva("60ef97e4-0001", "fw1", &protocol.Firmware{Mode: "uefi", SecureBoot: pb(false), TPM: "2.0", Interface: "hp", AdminPassword: pb(false)})
	salva("7aa604f7-0002", "fw2", &protocol.Firmware{Mode: "uefi", SecureBoot: pb(true), TPM: "1.2", Interface: "hp", AdminPassword: pb(true)})

	b := pagina(h, "GET", "/painel/inventario", sessaoTeste, nil).Body.String()
	for _, quer := range []string{
		`<span class="kpi-rot">BIOS sem senha</span><span class="kpi-val v-vermelho">1</span>`,
		`<span class="kpi-rot">Secure Boot desligado</span><span class="kpi-val v-laranja">1</span>`,
		`<span class="kpi-rot">TPM ausente ou 1.2</span><span class="kpi-val v-laranja">1</span>`,
		`<span class="kpi-rot">Senha sem informação</span><span class="kpi-val">0</span>`,
		`href="/painel/inventario?firmware=sem-senha"`, `>Senha da BIOS</a></th>`,
		`<td class="nivel-erro">não definida</td>`, `<td>definida</td>`,
	} {
		if !strings.Contains(b, quer) {
			t.Errorf("faltou %q", quer)
		}
	}
	// O filtro: só a sem senha; o cartão ativo leva de volta sem o filtro.
	b = pagina(h, "GET", "/painel/inventario?firmware=sem-senha", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `href="/painel/maquinas/60ef97e4-0001"`) || strings.Contains(b, `href="/painel/maquinas/7aa604f7-0002"`) ||
		!strings.Contains(b, `<a class="kpi ativo" href="/painel/inventario">`) || !strings.Contains(b, `name="firmware" value="sem-senha"`) {
		t.Errorf("filtro sem-senha:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/inventario?firmware=tpm", sessaoTeste, nil).Body.String(); !strings.Contains(b, "7aa604f7-0002") || strings.Contains(b, `href="/painel/maquinas/60ef97e4-0001"`) {
		t.Error("filtro tpm")
	}
	// Valor desconhecido: ignorado (lista inteira).
	if b := pagina(h, "GET", "/painel/inventario?firmware=<x>", sessaoTeste, nil).Body.String(); !strings.Contains(b, "7aa604f7-0002") || !strings.Contains(b, "60ef97e4-0001") {
		t.Error("filtro desconhecido")
	}
	// CSV: as colunas novas, com o mesmo filtro.
	csv := pagina(h, "GET", "/painel/inventario.csv?firmware=sem-senha", sessaoTeste, nil).Body.String()
	if !strings.Contains(csv, "firmware_modo;secure_boot;tpm;senha_admin_bios;senha_bios_fonte") || !strings.Contains(csv, ";uefi;nao;2.0;nao;hp") ||
		strings.Contains(csv, "7aa604f7-0002") {
		t.Errorf("CSV:\n%s", csv)
	}
}
