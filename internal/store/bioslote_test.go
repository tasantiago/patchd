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
	BIOSFleetSettings(ctx context.Context, pattern, strip string) (map[string]BIOSFleetSettings, error)
	CreateJob(ctx context.Context, j NewJob) error
	FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error)
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

// As configurações da BIOS para o padrão (docs/bios-config.md, seção 9): só as que casam com o
// filtro, pelo nome normalizado, e o último job bios_settings de cada máquina.
func testarBIOSPadrao(t *testing.T, st contratoBIOSLote) {
	ctx := context.Background()
	enum := func(name, value string) protocol.BIOSSetting {
		return protocol.BIOSSetting{Name: name, Value: value, Kind: protocol.BIOSKindEnum, Possible: []string{"Disable", "Enable"}}
	}
	inv := func(id string, fw *protocol.Firmware) {
		t.Helper()
		rep := protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, CollectedAt: time.Now().UTC(), OS: protocol.OSInfo{Hostname: id}}
		if fw != nil {
			rep.Hardware = &protocol.Hardware{Sources: []string{}, Firmware: fw}
		}
		if _, err := st.SaveInventory(ctx, id, rep); err != nil {
			t.Fatal(err)
		}
	}
	inv("p-1", &protocol.Firmware{Interface: "hp", Settings: []protocol.BIOSSetting{enum("Fast Boot", "Enable"), enum("Wake On LAN", "Disable"),
		enum("USB Storage Boot", "Enable"), enum("S5 Maximum Power-Savings", "Enable"), enum("UsbEmulation", "Enable")}})
	inv("p-2", &protocol.Firmware{Interface: "dell", Settings: []protocol.BIOSSetting{enum("UsbEmu", "Enabled")}})
	inv("p-3", &protocol.Firmware{Interface: "hp"}) // sem as configurações
	inv("p-4", nil)
	inv("p-5", &protocol.Firmware{Interface: "hp", Settings: []protocol.BIOSSetting{enum("Wake On LAN", "Disable")}})
	if _, err := st.RetireMachine(ctx, "p-5", "teste", "thyago"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	for i, j := range []NewJob{
		{ID: 9101, MachineID: "p-1", Type: "bios_settings", Signed: protocol.SignedJob{Payload: "primeiro", Signature: "x"}, CreatedBy: "t", NotAfter: t0.Add(time.Hour)},
		{ID: 9102, MachineID: "p-1", Type: "rescan", Signed: protocol.SignedJob{Payload: "outro tipo", Signature: "x"}, CreatedBy: "t", NotAfter: t0.Add(time.Hour)},
		{ID: 9103, MachineID: "p-2", Type: "bios_settings", Signed: protocol.SignedJob{Payload: "da p-2", Signature: "x"}, CreatedBy: "t", NotAfter: t0.Add(time.Hour)},
	} {
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	if _, err := st.FinishJob(ctx, 9103, "falhou", "teste", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	l, err := st.BIOSFleetSettings(ctx, `wakeonlan|maximumpowersaving|usbstorageboot|^usbemu$`, " -_/\\\t().,:")
	if err != nil {
		t.Fatal(err)
	}
	if _, aposentada := l["p-5"]; aposentada || len(l) < 4 {
		t.Fatalf("frota: %d máquinas, aposentada=%v", len(l), aposentada)
	}
	p1 := l["p-1"]
	if !p1.Read || len(p1.Settings) != 3 || p1.Settings[0].Name != "Wake On LAN" || p1.Settings[1].Name != "USB Storage Boot" ||
		p1.Settings[2].Name != "S5 Maximum Power-Savings" || len(p1.Settings[0].Possible) != 2 || p1.Settings[0].Kind != protocol.BIOSKindEnum {
		t.Errorf("p-1 (o filtro pelo nome normalizado, na ordem da BIOS): %+v", p1.Settings)
	}
	if p1.LastJob != 9101 || p1.LastState != "pendente" || p1.LastFinished != nil || p1.LastPayload != "primeiro" {
		t.Errorf("p-1, último job: %+v", p1)
	}
	if p2 := l["p-2"]; !p2.Read || len(p2.Settings) != 1 || p2.LastJob != 9103 || p2.LastState != "falhou" || p2.LastFinished == nil || p2.LastPayload != "da p-2" {
		t.Errorf("p-2: %+v", p2)
	}
	if l["p-3"].Read || l["p-4"].Read || len(l["p-3"].Settings) != 0 || l["p-4"].LastJob != 0 {
		t.Errorf("sem as configurações: %+v %+v", l["p-3"], l["p-4"])
	}
}

func TestBIOSPadraoMemoria(t *testing.T) { testarBIOSPadrao(t, NewMemory()) }

func TestBIOSPadraoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarBIOSPadrao(t, NewPostgres(pool))
}

func TestBIOSLoteMemoria(t *testing.T) { testarBIOSLote(t, NewMemory()) }

func TestBIOSLotePostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarBIOSLote(t, NewPostgres(pool))
}
