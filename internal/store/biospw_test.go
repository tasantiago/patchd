package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoGuardaBIOS interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	CreateBIOSPassword(ctx context.Context, b BIOSPassword) (BIOSPassword, bool, error)
	BIOSPasswords(ctx context.Context, machineID string) ([]BIOSPassword, error)
	BIOSPasswordByID(ctx context.Context, id int64) (BIOSPassword, bool, error)
	MarkBIOSRevealed(ctx context.Context, id int64, by string, at time.Time) error
}

// Aula 21.4: a guarda guarda envelopes; uma pendente por máquina; máquina fora da frota não
// ganha senha nova; a revelação é contada.
func testarGuardaBIOS(t *testing.T, st contratoGuardaBIOS) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-0001", "b-0002"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + id, OS: protocol.OSInfo{Hostname: id}}); err != nil {
			t.Fatal(err)
		}
	}
	nova := func(id string, env byte) BIOSPassword {
		return BIOSPassword{MachineID: id, Escrow: []byte{env, 1, 2}, EscrowKeyID: "0123456789ab", CreatedAt: t0, CreatedBy: "admin"}
	}
	a, criada, err := st.CreateBIOSPassword(ctx, nova("a-0001", 7))
	if err != nil || !criada || a.ID == 0 || a.State != BIOSPending || a.UpdatedAt != t0 {
		t.Fatalf("criar: %+v %v %v", a, criada, err)
	}
	// Segunda: a pendente que já existe volta, a nova não é gravada.
	de, criada, err := st.CreateBIOSPassword(ctx, nova("a-0001", 9))
	if err != nil || criada || de.ID != a.ID || de.Escrow[0] != 7 {
		t.Fatalf("segunda: %+v %v %v", de, criada, err)
	}
	b, _, _ := st.CreateBIOSPassword(ctx, nova("b-0002", 8))
	if _, _, err := st.CreateBIOSPassword(ctx, nova("nao-existe", 1)); !errors.Is(err, ErrNoMachine) {
		t.Errorf("máquina inexistente: %v", err)
	}
	if _, err := st.RetireMachine(ctx, "b-0002", "teste", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateBIOSPassword(ctx, nova("b-0002", 1)); !errors.Is(err, ErrNoMachine) {
		t.Errorf("aposentada: %v", err)
	}
	// A aposentada continua com a guarda (a senha segue na placa).
	if l, err := st.BIOSPasswords(ctx, "b-0002"); err != nil || len(l) != 1 || l[0].ID != b.ID {
		t.Errorf("lista da aposentada: %+v %v", l, err)
	}
	if l, _ := st.BIOSPasswords(ctx, "a-0001"); len(l) != 1 || l[0].EscrowKeyID != "0123456789ab" || l[0].CreatedBy != "admin" {
		t.Errorf("lista: %+v", l)
	}
	t1 := t0.Add(time.Hour)
	if err := st.MarkBIOSRevealed(ctx, a.ID, "outro", t1); err != nil {
		t.Fatal(err)
	}
	_ = st.MarkBIOSRevealed(ctx, a.ID, "outro", t1)
	got, found, err := st.BIOSPasswordByID(ctx, a.ID)
	if err != nil || !found || got.Reveals != 2 || got.LastRevealedAt == nil || !got.LastRevealedAt.Equal(t1) || got.LastRevealedBy != "outro" {
		t.Errorf("revelada: %+v %v %v", got, found, err)
	}
	if _, found, _ := st.BIOSPasswordByID(ctx, 9999); found {
		t.Error("ID inexistente encontrado")
	}
}

func TestGuardaBIOSMemoria(t *testing.T) { testarGuardaBIOS(t, NewMemory()) }

func TestGuardaBIOSPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarGuardaBIOS(t, NewPostgres(pool))
}
