package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoCheckin interface {
	contratoEnrollment
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	CheckIn(ctx context.Context, id, agentVersion, inventoryHash string) (bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
}

func testarCheckin(t *testing.T, st contratoCheckin) {
	ctx := context.Background()
	_, h := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, h, "checkin", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	m := comChaves("4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b", "", "")
	if _, err := st.Enroll(ctx, h, m); err != nil {
		t.Fatal(err)
	}
	inv := inv("aaaa", "1")

	if known, err := st.CheckIn(ctx, m.ID, "v0.4.0", inv.Hash); err != nil || known {
		t.Fatalf("sem inventário guardado: %t %v", known, err)
	}
	if _, err := st.SaveInventory(ctx, m.ID, inv); err != nil {
		t.Fatal(err)
	}
	antes, _ := st.Machines(ctx)
	time.Sleep(10 * time.Millisecond)
	if known, err := st.CheckIn(ctx, m.ID, "v0.4.0", inv.Hash); err != nil || !known {
		t.Fatalf("com o mesmo hash: %t %v", known, err)
	}
	depois, _ := st.Machines(ctx)
	if !depois[0].LastSeenAt.After(antes[0].LastSeenAt) {
		t.Errorf("o check-in atualiza o último contato: %v → %v", antes[0].LastSeenAt, depois[0].LastSeenAt)
	}
	if known, _ := st.CheckIn(ctx, m.ID, "v0.4.0", "sha256:outro"); known {
		t.Error("hash diferente não é conhecido")
	}
}

func TestCheckinMemoria(t *testing.T) { testarCheckin(t, NewMemory()) }

func TestCheckinPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarCheckin(t, NewPostgres(pool))
}

func TestPruneScansPreservaAAtual(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	for _, id := range []string{"a", "b"} {
		for range 3 {
			if err := st.SaveScan(ctx, id, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Envelhece todas as buscas: 100 dias atrás.
	if _, err := pool.Exec(ctx, "UPDATE scan_reports SET received_at = now() - interval '100 days'"); err != nil {
		t.Fatal(err)
	}
	n, err := st.PruneScans(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 4 {
		t.Fatalf("esperadas 4 buscas apagadas (as antigas, não as atuais): %d %v", n, err)
	}
	for _, id := range []string{"a", "b"} {
		if _, found, err := st.LatestScan(ctx, id); !found || err != nil {
			t.Errorf("a busca atual de %s precisa sobreviver: %t %v", id, found, err)
		}
	}
}
