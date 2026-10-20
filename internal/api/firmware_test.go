package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

func pb(b bool) *bool { return &b }

// TestFirmwareNoPainel (Aula 21.1): o cartão Firmware na aba Hardware, com o destaque do que
// merece atenção; e o servidor descarta um firmware com valor desconhecido sem perder o resto.
func TestFirmwareNoPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)

	envia1 := func(fw *protocol.Firmware, hashHex string) {
		t.Helper()
		rep := protocol.InventoryReport{SchemaVersion: protocol.InventorySchemaVersion, AgentVersion: "v0.18.0",
			CollectedAt: time.Now().UTC(), Hash: "sha256:" + strings.Repeat(hashHex, 64),
			OS:       protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Hostname: "pc-lab-01"},
			Hardware: &protocol.Hardware{Manufacturer: "HP", Model: "EliteDesk 800 G6", Firmware: fw, Sources: []string{}},
			Software: []protocol.Software{}}
		body, _ := json.Marshal(rep)
		if r := envia(h, "POST", "/api/v1/agent/inventory", maq.Credential, body); r.Code >= 300 {
			t.Fatalf("inventário: %d %s", r.Code, r.Body)
		}
	}
	// A máquina da frota de teste (a página da máquina vem dela).
	if _, err := st.SaveInventory(ctx, "60ef97e4-0001", protocol.InventoryReport{SchemaVersion: 1, Hash: "fw-a",
		OS: protocol.OSInfo{Family: "windows", Hostname: "vm"},
		Hardware: &protocol.Hardware{Manufacturer: "HP", Model: "EliteDesk 800 G6", Sources: []string{},
			Firmware: &protocol.Firmware{Mode: "uefi", SecureBoot: pb(false), TPM: "2.0", Interface: "hp",
				AdminPassword: pb(false), PowerOnPassword: pb(false)}}}); err != nil {
		t.Fatal(err)
	}
	b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil).Body.String()
	for _, quer := range []string{"<h2>Firmware</h2>", "<dt>Modo</dt><dd>UEFI</dd>", `<dt>Secure Boot</dt><dd class="nivel-atencao">desligado</dd>`,
		"<dt>TPM</dt><dd>2.0</dd>", `<dt>Senha de administrador da BIOS</dt><dd class="nivel-erro">NÃO definida`, "HP (WMI da BIOS)"} {
		if !strings.Contains(b, quer) {
			t.Errorf("faltou %q", quer)
		}
	}

	// Valor desconhecido: o firmware fica de fora, o inventário entra, e o motivo fica nos erros.
	envia1(&protocol.Firmware{Mode: "<script>", TPM: "2.0"}, "b")
	inv, _, _ := st.LatestInventory(ctx, maq.MachineID)
	if inv.Hardware == nil || inv.Hardware.Firmware != nil || inv.Hardware.Model != "EliteDesk 800 G6" ||
		!strings.Contains(strings.Join(inv.Errors, ";"), "firmware: descartado") {
		t.Errorf("descartado: %+v %v", inv.Hardware, inv.Errors)
	}
	// Sem firmware no inventário (agente anterior, macOS): o cartão não aparece.
	_, _ = st.SaveInventory(ctx, "60ef97e4-0001", protocol.InventoryReport{SchemaVersion: 1, Hash: "fw-b",
		OS: protocol.OSInfo{Family: "windows", Hostname: "vm"}, Hardware: &protocol.Hardware{Manufacturer: "HP", Sources: []string{}}})
	if r := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil); r.Code != http.StatusOK ||
		strings.Contains(r.Body.String(), "<h2>Firmware</h2>") {
		t.Errorf("sem firmware: %d", r.Code)
	}
}
