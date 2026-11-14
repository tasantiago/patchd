package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoCheckinTudo interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	CheckInAll(ctx context.Context, id string, c Checkin) (bool, error)
	HostState(ctx context.Context, id string) (HostState, bool, error)
	CheckinTLS(ctx context.Context, id string) (*bool, error)
	CheckinRelay(ctx context.Context, id string) (*bool, error)
	MachineCapabilities(ctx context.Context, id string) ([]string, error)
	CreateWakeRequest(ctx context.Context, r WakeRequest) (int64, error)
	MarkWakeSent(ctx context.Context, jobID int64, relay string, at time.Time) (bool, error)
	WakeRequests(ctx context.Context, machineID string, limit int) ([]WakeRequest, error)
}

// O check-in inteiro numa ida ao banco: o contato, o canal, o relé, as capacidades, o estado,
// o endereço e a resposta ao "ligar pela rede". Sem estado (agente antigo, ou estado
// recusado) e sem endereço legível, o que estava guardado fica.
func testarCheckinTudo(t *testing.T, st contratoCheckinTudo) {
	ctx := context.Background()
	const id = "chk-0001"
	if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:aa", OS: protocol.OSInfo{Hostname: "pc"}}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	if _, err := st.CreateWakeRequest(ctx, WakeRequest{MachineID: id, RequestedAt: t0, RequestedBy: "ana", Network: "192.0.2.0/24",
		MACs: []string{"00:11:22:33:44:55"}, Relays: []string{"rep-1"}, JobIDs: []int64{7}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.MarkWakeSent(ctx, 7, "rep-1", t0.Add(time.Minute)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	estado := &protocol.MachineState{CollectedAt: t0,
		Interfaces: []protocol.NetInterface{{Name: "eth0", Up: true, IPv4: []string{"192.0.2.10/24"}}}, Disks: []protocol.Disk{{Mount: "C:", TotalBytes: 100, FreeBytes: 40}}}
	known, err := st.CheckInAll(ctx, id, Checkin{AgentVersion: "v1.2.3", InventoryHash: "sha256:aa", TLS: true, Relay: true,
		Capabilities: []string{"jobs-v1", "terminal-v1"}, State: estado, Addr: "203.0.113.7", At: t0.Add(5 * time.Minute)})
	if err != nil || !known {
		t.Fatalf("check-in com o hash atual: %v %v", known, err)
	}
	h, found, err := st.HostState(ctx, id)
	if err != nil || !found || h.State == nil || len(h.State.Interfaces) != 1 || h.State.Interfaces[0].IPv4[0] != "192.0.2.10/24" || h.CheckinAddr != "203.0.113.7" {
		t.Fatalf("estado e endereço: %+v %v %v", h, found, err)
	}
	if v, _ := st.CheckinTLS(ctx, id); v == nil || !*v {
		t.Errorf("canal: %v", v)
	}
	if v, _ := st.CheckinRelay(ctx, id); v == nil || !*v {
		t.Errorf("relé: %v", v)
	}
	if caps, _ := st.MachineCapabilities(ctx, id); len(caps) != 2 || caps[0] != "jobs-v1" || caps[1] != "terminal-v1" {
		t.Errorf("capacidades: %v", caps)
	}
	if l, _ := st.WakeRequests(ctx, id, 5); len(l) != 1 || l[0].AnsweredAt == nil || !l[0].AnsweredAt.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("a resposta ao ligar pela rede: %+v", l)
	}

	// Outro hash, sem estado, endereço com zona (IPv6 de enlace) e depois ilegível, sem relé,
	// sem capacidades: o estado e o último endereço legível ficam; o resto muda.
	known, err = st.CheckInAll(ctx, id, Checkin{AgentVersion: "v1.2.4", InventoryHash: "sha256:bb", Addr: "fe80::1%eth0", At: t0.Add(time.Hour)})
	if err != nil || known {
		t.Fatalf("check-in com outro hash: %v %v", known, err)
	}
	if h, _, _ = st.HostState(ctx, id); h.State == nil || h.CheckinAddr != "fe80::1" {
		t.Errorf("sem estado novo, o anterior fica; a zona do endereço sai: %+v", h)
	}
	if _, err = st.CheckInAll(ctx, id, Checkin{AgentVersion: "v1.2.4", Addr: "não é endereço", At: t0.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if h, _, _ = st.HostState(ctx, id); h.State == nil || h.CheckinAddr != "fe80::1" {
		t.Errorf("endereço ilegível não apaga o anterior: %+v", h)
	}
	if v, _ := st.CheckinTLS(ctx, id); v == nil || *v {
		t.Errorf("canal depois do check-in por HTTP: %v", v)
	}
	if v, _ := st.CheckinRelay(ctx, id); v == nil || *v {
		t.Errorf("relé depois do check-in direto: %v", v)
	}
	if caps, _ := st.MachineCapabilities(ctx, id); len(caps) != 0 {
		t.Errorf("capacidades do agente que não anuncia nenhuma: %v", caps)
	}
	// Máquina que não existe: erro, como no check-in de sempre.
	if _, err := st.CheckInAll(ctx, "nao-existe", Checkin{AgentVersion: "v1", At: t0}); err == nil {
		t.Error("check-in de máquina desconhecida passou")
	}
}

func TestCheckinTudoMemoria(t *testing.T) { testarCheckinTudo(t, NewMemory()) }

func TestCheckinTudoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarCheckinTudo(t, NewPostgres(pool))
}
