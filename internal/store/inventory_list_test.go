package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoInventario interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	InventoryList(ctx context.Context) ([]InventoryRow, error)
}

func testarListaDeInventario(t *testing.T, st contratoInventario) {
	ctx := context.Background()
	coleta := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	rep := func(host, hash string, hw *protocol.Hardware) protocol.InventoryReport {
		return protocol.InventoryReport{SchemaVersion: 1, Hash: hash, CollectedAt: coleta, Hardware: hw,
			OS: protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Version: "25H2", Hostname: host}}
	}
	if _, err := st.SaveInventory(ctx, "b-0002", rep("pc-b", "sha256:b", &protocol.Hardware{
		Manufacturer: "Dell Inc.", Model: "OptiPlex 7010", SerialNumber: "ABC1234", ChassisType: "3"})); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveInventory(ctx, "a-0001", rep("pc-a", "sha256:a", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveInventory(ctx, "c-0003", rep("pc-c", "sha256:c", nil)); err != nil {
		t.Fatal(err)
	}
	s := &protocol.MachineState{CollectedAt: coleta, Interfaces: []protocol.NetInterface{{Name: "eth0", IPv4: []string{"192.0.2.7/24"}, Up: true}}}
	if err := st.SaveHostState(ctx, "b-0002", s, "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetireMachine(ctx, "c-0003", "teste", "teste"); err != nil {
		t.Fatal(err)
	}

	list, err := st.InventoryList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a-0001" || list[1].ID != "b-0002" {
		t.Fatalf("a frota em ordem de ID, sem as aposentadas: %+v", list)
	}
	a, b := list[0], list[1]
	if a.Hostname != "pc-a" || a.Manufacturer != "" || a.State != nil || a.CheckinAddr != "" || a.Ring != DefaultRing {
		t.Errorf("sem hardware nem estado: %+v", a)
	}
	if b.Hostname != "pc-b" || b.OSName != "Windows 11 Pro" || b.OSVersion != "25H2" ||
		b.Manufacturer != "Dell Inc." || b.Model != "OptiPlex 7010" || b.Serial != "ABC1234" || b.ChassisType != "3" ||
		b.CollectedAt == nil || !b.CollectedAt.Equal(coleta) || b.LastSeenAt.IsZero() ||
		b.State == nil || b.State.Interfaces[0].IPv4[0] != "192.0.2.7/24" || b.CheckinAddr != "192.0.2.7" {
		t.Errorf("com hardware e estado: %+v", b)
	}

	// Inventário novo: as colunas acompanham o inventário atual.
	if _, err := st.SaveInventory(ctx, "b-0002", rep("pc-b", "sha256:b2", &protocol.Hardware{Model: "OptiPlex 7020", ChassisType: "35"})); err != nil {
		t.Fatal(err)
	}
	list, _ = st.InventoryList(ctx)
	if list[1].Model != "OptiPlex 7020" || list[1].Serial != "" || list[1].ChassisType != "35" {
		t.Errorf("hardware do inventário atual: %+v", list[1])
	}
}

func TestListaDeInventarioMemoria(t *testing.T) { testarListaDeInventario(t, NewMemory()) }

func TestListaDeInventarioPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarListaDeInventario(t, NewPostgres(pool))
}
