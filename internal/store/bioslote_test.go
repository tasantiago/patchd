package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoBIOSLote interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (bool, error)
	SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error
	SetMachineRing(ctx context.Context, id string, ring int) (bool, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	CreateBIOSPassword(ctx context.Context, b BIOSPassword) (BIOSPassword, bool, error)
	BIOSFleet(ctx context.Context) ([]BIOSFleetRow, error)
}

// A frota para a senha da BIOS em lote (docs/bios-senha.md, seção 12): o firmware do inventário
// atual, os avisos do estado e as senhas da guarda, na ordem do nome, sem as aposentadas.
func testarBIOSLote(t *testing.T, st contratoBIOSLote) {
	ctx := context.Background()
	sim, nao := true, false
	inv := func(id, nome string, fw *protocol.Firmware) {
		t.Helper()
		rep := protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, CollectedAt: time.Now().UTC(), OS: protocol.OSInfo{Hostname: nome}}
		if fw != nil {
			rep.Hardware = &protocol.Hardware{Sources: []string{}, Firmware: fw}
		}
		if _, err := st.SaveInventory(ctx, id, rep); err != nil {
			t.Fatal(err)
		}
	}
	inv("m-3", "pc-zeta", &protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &nao})
	inv("m-1", "PC-alfa", &protocol.Firmware{Interface: "lenovo", AdminPassword: &nao})
	inv("m-2", "pc-beta", nil)
	inv("m-4", "pc-fora", &protocol.Firmware{Interface: "hp", AdminPassword: &sim})
	if err := st.SaveHostState(ctx, "m-3", &protocol.MachineState{BoxKey: "chave-da-caixa", BIOSLegacy: true, BIOSCopy: true}, "192.0.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveHostState(ctx, "m-1", &protocol.MachineState{}, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetMachineRing(ctx, "m-3", 4); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"m-3", "m-4"} {
		if _, _, err := st.CreateBIOSPassword(ctx, BIOSPassword{MachineID: id, Escrow: []byte{7}, EscrowKeyID: "0123456789ab", CreatedAt: t0, CreatedBy: "thyago"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RetireMachine(ctx, "m-4", "teste", "thyago"); err != nil {
		t.Fatal(err)
	}

	l, err := st.BIOSFleet(ctx)
	if err != nil || len(l) != 3 {
		t.Fatalf("frota: %+v %v", l, err)
	}
	if l[0].MachineID != "m-1" || l[1].MachineID != "m-2" || l[2].MachineID != "m-3" {
		t.Errorf("ordem pelo nome, sem diferenciar maiúsculas: %s %s %s", l[0].Hostname, l[1].Hostname, l[2].Hostname)
	}
	if a := l[0]; a.Interface != "lenovo" || a.AdminPassword == nil || *a.AdminPassword || a.SureAdmin != nil || a.BoxKey != "" || a.Legacy || a.CopyOK ||
		len(a.Passwords) != 0 || a.Ring != DefaultRing {
		t.Errorf("m-1: %+v", a)
	}
	if b := l[1]; b.Interface != "" || b.AdminPassword != nil || b.Hostname != "pc-beta" {
		t.Errorf("m-2 (sem firmware): %+v", b)
	}
	if z := l[2]; z.Interface != "hp" || z.AdminPassword == nil || !*z.AdminPassword || z.SureAdmin == nil || *z.SureAdmin || z.BoxKey != "chave-da-caixa" ||
		!z.Legacy || !z.CopyOK || z.Ring != 4 || len(z.Passwords) != 1 || z.Passwords[0].State != BIOSPending || z.Passwords[0].Escrow[0] != 7 {
		t.Errorf("m-3: %+v", z)
	}
}

func TestBIOSLoteMemoria(t *testing.T) { testarBIOSLote(t, NewMemory()) }

func TestBIOSLotePostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarBIOSLote(t, NewPostgres(pool))
}
