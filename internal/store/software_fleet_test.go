package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoSoftwareParque interface {
	CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error)
	Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) ([]protocol.IdentityLink, error)
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	FleetSoftware(ctx context.Context) ([]SoftwareRow, error)
	SoftwareInstalls(ctx context.Context, osFamily string, names []string, publisher string) ([]SoftwareInstall, error)
}

// Os programas do parque: do inventário atual de cada máquina, sem as aposentadas.
func testarSoftwareParque(t *testing.T, st contratoSoftwareParque) {
	ctx := context.Background()
	_, tok := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, tok, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	inv := func(host string, sw ...protocol.Software) string {
		t.Helper()
		m, _ := novaMaquina()
		m.Evidence.InstallID = "guid-" + host
		if _, err := st.Enroll(ctx, tok, m); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveInventory(ctx, m.ID, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + host, CollectedAt: time.Now().UTC(),
			OS: protocol.OSInfo{Family: "windows", ID: "windows", Name: "Windows 11", Hostname: host}, Software: sw}); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	lo := protocol.Software{Name: "LibreOffice 26.8.3.1", Version: "26.8.3.1", Publisher: "The Document Foundation", Scope: "machine", Uninstall: "msi",
		ProductCode: "{11111111-2222-3333-4444-555555555555}", Source: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{11111111-2222-3333-4444-555555555555}`}
	inv("pc-a", lo, protocol.Software{Name: "KB123", Version: "1", Scope: "machine", IsUpdate: true})
	inv("pc-b", lo)
	velha := inv("pc-c", lo)
	if _, err := st.RetireMachine(ctx, velha, "teste", "t"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.FleetSoftware(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SoftwareRow{}
	for _, r := range rows {
		got[r.Name] = r
	}
	if r := got["LibreOffice 26.8.3.1"]; r.Machines != 2 || r.OSFamily != "windows" || r.Uninstall != "msi" || r.System || r.Scope != "machine" {
		t.Errorf("LibreOffice: %+v", r)
	}
	if r := got["KB123"]; !r.System || r.Machines != 1 {
		t.Errorf("atualização: %+v", r)
	}
	ins, err := st.SoftwareInstalls(ctx, "windows", []string{"LibreOffice 26.8.3.1"}, "The Document Foundation")
	if err != nil || len(ins) != 2 || ins[0].Hostname != "pc-a" || ins[1].Hostname != "pc-b" || ins[0].Uninstall != "msi" ||
		ins[0].ProductCode != lo.ProductCode || ins[0].Source != lo.Source {
		t.Fatalf("máquinas: %+v %v", ins, err)
	}
	if ins, _ := st.SoftwareInstalls(ctx, "windows", []string{"LibreOffice 26.8.3.1"}, "Outro"); len(ins) != 0 {
		t.Error("outro fabricante")
	}
}

func TestSoftwareParqueMemoria(t *testing.T) { testarSoftwareParque(t, NewMemory()) }

func TestSoftwareParquePostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarSoftwareParque(t, NewPostgres(pool))
}
