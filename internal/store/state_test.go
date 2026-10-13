package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoEstado interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error
	HostState(ctx context.Context, id string) (HostState, bool, error)
}

// Endereços de documentação (RFC 5737 e 3849).
func testarEstado(t *testing.T, st contratoEstado) {
	ctx := context.Background()
	const id = "60ef97e4-0001"
	if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:a"}); err != nil {
		t.Fatal(err)
	}
	h, found, err := st.HostState(ctx, id)
	if err != nil || !found || h.State != nil || h.CheckinAddr != "" {
		t.Fatalf("antes do primeiro check-in: %+v %t %v", h, found, err)
	}

	s := &protocol.MachineState{CollectedAt: time.Now().UTC().Truncate(time.Second),
		Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: "00:15:5d:01:02:03", IPv4: []string{"192.0.2.10/24"}, Up: true}},
		Disks:      []protocol.Disk{{Mount: "/", FS: "ext4", Device: "/dev/sda2", TotalBytes: 100 << 30, FreeBytes: 40 << 30}}}
	// Socket dual-stack: o IPv4 chega mapeado em IPv6 e é guardado como IPv4.
	if err := st.SaveHostState(ctx, id, s, "::ffff:192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	h, _, err = st.HostState(ctx, id)
	if err != nil || h.State == nil || h.CheckinAddr != "192.0.2.10" || h.ReceivedAt.IsZero() ||
		len(h.State.Disks) != 1 || h.State.Disks[0].TotalBytes != 100<<30 || h.State.Interfaces[0].IPv4[0] != "192.0.2.10/24" ||
		!h.State.CollectedAt.Equal(s.CollectedAt) {
		t.Fatalf("lido de volta: %+v %v", h, err)
	}
	primeiro := h.ReceivedAt

	// Um agente antigo (sem estado) só atualiza o endereço; o estado anterior fica.
	time.Sleep(10 * time.Millisecond)
	if err := st.SaveHostState(ctx, id, nil, "2001:db8::7"); err != nil {
		t.Fatal(err)
	}
	h, _, _ = st.HostState(ctx, id)
	if h.State == nil || h.CheckinAddr != "2001:db8::7" || !h.ReceivedAt.Equal(primeiro) {
		t.Errorf("sem estado no check-in: %+v", h)
	}
	// Endereço que não é IP (ex.: socket unix, teste) não é gravado nem derruba nada.
	if err := st.SaveHostState(ctx, id, nil, "@"); err != nil {
		t.Fatal(err)
	}
	if h, _, _ = st.HostState(ctx, id); h.CheckinAddr != "2001:db8::7" {
		t.Errorf("endereço inválido sobrescreveu: %q", h.CheckinAddr)
	}
	// Estado novo substitui o anterior inteiro: não há histórico.
	s2 := &protocol.MachineState{CollectedAt: time.Now().UTC(), Interfaces: []protocol.NetInterface{}, Disks: []protocol.Disk{}}
	if err := st.SaveHostState(ctx, id, s2, "192.0.2.11"); err != nil {
		t.Fatal(err)
	}
	if h, _, _ = st.HostState(ctx, id); h.State == nil || len(h.State.Disks) != 0 || !h.ReceivedAt.After(primeiro) {
		t.Errorf("estado novo: %+v", h)
	}
	if _, found, err := st.HostState(ctx, "ffffffff-0009"); found || err != nil {
		t.Errorf("máquina desconhecida: %t %v", found, err)
	}
	if err := st.SaveHostState(ctx, "ffffffff-0009", s, "192.0.2.1"); err != nil {
		t.Errorf("máquina desconhecida: gravar não é erro (o check-in já conferiu a credencial): %v", err)
	}
}

func TestEstadoMemoria(t *testing.T) { testarEstado(t, NewMemory()) }

func TestEstadoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarEstado(t, NewPostgres(pool))
}
