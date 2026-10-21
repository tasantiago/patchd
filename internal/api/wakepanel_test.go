package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// TestLigarPelaRedeNoPainel (Aula 22.1): o cartão com a decisão e o que pesou; o servidor
// descarta um bloco inválido sem perder o resto do inventário.
func TestLigarPelaRedeNoPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	salva := func(hash string, w *protocol.Wake) string {
		t.Helper()
		if _, err := st.SaveInventory(ctx, "60ef97e4-0001", protocol.InventoryReport{SchemaVersion: 1, Hash: hash,
			OS:       protocol.OSInfo{Family: "windows", Hostname: "vm"},
			Hardware: &protocol.Hardware{Manufacturer: "HP", ChassisType: "10", Sources: []string{}, Wake: w}}); err != nil {
			t.Fatal(err)
		}
		return pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil).Body.String()
	}
	b := salva("w1", &protocol.Wake{BIOS: pb(false), DeepSleep: pb(false), FastStartup: pb(true), BitLockerPIN: pb(false),
		Settings: []protocol.WakeSetting{{Name: "S5 Wake On LAN", Value: "Disable"}},
		Adapters: []protocol.WakeAdapter{{Name: "Ethernet", MAC: "00:11:22:33:44:55", Wired: true, MagicPacket: protocol.WakeOn, DeviceWake: pb(true)}}})
	for _, quer := range []string{"<h2>Ligar pela rede (Wake on LAN)</h2>", `<dd class="nivel-erro">bloqueada</dd>`,
		"<li>Wake on LAN desligado na BIOS</li>", "<li>notebook: só acorda na tomada e no cabo (ou na dock)</li>",
		"S5 Wake On LAN: <code>Disable</code>", "<td>Ethernet</td><td><code>00:11:22:33:44:55</code></td><td>sim</td><td>ligado</td><td>sim</td>",
		"<dt>Fast Startup</dt><dd>ligado</dd>"} {
		if !strings.Contains(b, quer) {
			t.Errorf("faltou %q", quer)
		}
	}
	b = salva("w2", &protocol.Wake{BIOS: pb(true), Adapters: []protocol.WakeAdapter{{Name: "Ethernet", Wired: true, MagicPacket: protocol.WakeOn, DeviceWake: pb(true)}}})
	if !strings.Contains(b, `<dd class="nivel-ok">pronta</dd>`) {
		t.Error("pronta")
	}
	if b := salva("w3", nil); strings.Contains(b, "Ligar pela rede") {
		t.Error("sem a coleta: sem o cartão")
	}

	// Pela API do agente: bloco inválido fora, o resto dentro.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	rep := protocol.InventoryReport{SchemaVersion: protocol.InventorySchemaVersion, AgentVersion: "v0.18.5", CollectedAt: time.Now().UTC(),
		Hash: "sha256:" + strings.Repeat("c", 64), OS: protocol.OSInfo{Family: "windows", Hostname: "pc"},
		Hardware: &protocol.Hardware{Manufacturer: "HP", Model: "Exemplo", Sources: []string{},
			Wake: &protocol.Wake{Adapters: []protocol.WakeAdapter{{Name: "Ethernet", MagicPacket: "<script>"}}}},
		Software: []protocol.Software{}}
	body, _ := json.Marshal(rep)
	if r := envia(h, "POST", "/api/v1/agent/inventory", maq.Credential, body); r.Code >= 300 {
		t.Fatalf("inventário: %d %s", r.Code, r.Body)
	}
	inv, _, _ := st.LatestInventory(ctx, maq.MachineID)
	if inv.Hardware == nil || inv.Hardware.Wake != nil || inv.Hardware.Model != "Exemplo" ||
		!strings.Contains(strings.Join(inv.Errors, ";"), "ligar pela rede: descartado") {
		t.Errorf("descartado: %+v %v", inv.Hardware, inv.Errors)
	}
}
