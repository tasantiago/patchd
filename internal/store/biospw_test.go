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
	AttachBIOSJob(ctx context.Context, id, jobID int64, at time.Time) (bool, error)
	SettleBIOSPassword(ctx context.Context, id, jobID int64, state, result string, at time.Time) (bool, error)
	ResolveBIOSPassword(ctx context.Context, id int64, newValid bool, result string, at time.Time) (bool, error)
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

// Aula 21.5: a gravação. Job ligado uma vez; fechamento só com o job certo; a ativa nova
// substitui a anterior; recusa devolve a pendente; incerta resolvida pelo admin.
func testarGravacaoBIOS(t *testing.T, st contratoGuardaBIOS) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	if _, err := st.SaveInventory(ctx, "c-0003", protocol.InventoryReport{SchemaVersion: 1, Hash: "hc", OS: protocol.OSInfo{Hostname: "c"}}); err != nil {
		t.Fatal(err)
	}
	nova := func() BIOSPassword {
		b, _, err := st.CreateBIOSPassword(ctx, BIOSPassword{MachineID: "c-0003", Escrow: []byte{1}, EscrowKeyID: "0123456789ab", CreatedAt: t0, CreatedBy: "a"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	estado := func(id int64) BIOSPassword {
		b, _, _ := st.BIOSPasswordByID(ctx, id)
		return b
	}
	b1 := nova()
	if ok, err := st.AttachBIOSJob(ctx, b1.ID, 50, t0); !ok || err != nil {
		t.Fatalf("ligar: %v %v", ok, err)
	}
	if ok, _ := st.AttachBIOSJob(ctx, b1.ID, 51, t0); ok {
		t.Error("ligou um segundo job")
	}
	if ok, _ := st.SettleBIOSPassword(ctx, b1.ID, 49, BIOSActive, "x", t0); ok {
		t.Error("fechou com o job errado")
	}
	if _, err := st.SettleBIOSPassword(ctx, b1.ID, 50, BIOSReplaced, "x", t0); err == nil {
		t.Error("fechamento como substituída aceito")
	}
	if ok, err := st.SettleBIOSPassword(ctx, b1.ID, 50, BIOSActive, "gravada (hp, código 0)", t0); !ok || err != nil {
		t.Fatalf("ativar: %v %v", ok, err)
	}
	if b := estado(b1.ID); b.State != BIOSActive || b.Result != "gravada (hp, código 0)" || b.JobID != 50 {
		t.Errorf("ativa: %+v", b)
	}
	if ok, _ := st.SettleBIOSPassword(ctx, b1.ID, 50, BIOSFailed, "x", t0); ok {
		t.Error("fechou duas vezes")
	}
	// Recusa: volta a pendente, sem job, e pode ganhar outro.
	b2 := nova()
	_, _ = st.AttachBIOSJob(ctx, b2.ID, 60, t0)
	if ok, _ := st.SettleBIOSPassword(ctx, b2.ID, 60, BIOSPending, "recusado: Sure Admin", t0); !ok {
		t.Fatal("devolver")
	}
	if b := estado(b2.ID); b.State != BIOSPending || b.JobID != 0 || b.Result != "recusado: Sure Admin" {
		t.Errorf("devolvida: %+v", b)
	}
	if ok, _ := st.AttachBIOSJob(ctx, b2.ID, 61, t0); !ok {
		t.Error("a devolvida não ganhou job novo")
	}
	// Incerta: a anterior continua ativa até a decisão.
	_, _ = st.SettleBIOSPassword(ctx, b2.ID, 61, BIOSUncertain, "resultado incerto", t0)
	if b := estado(b1.ID); b.State != BIOSActive {
		t.Errorf("a anterior durante a incerta: %+v", b)
	}
	if ok, _ := st.ResolveBIOSPassword(ctx, b1.ID, true, "x", t0); ok {
		t.Error("resolveu uma linha que não está incerta")
	}
	if ok, err := st.ResolveBIOSPassword(ctx, b2.ID, true, "conferida na BIOS", t0); !ok || err != nil {
		t.Fatalf("resolver: %v %v", ok, err)
	}
	if a, b := estado(b1.ID), estado(b2.ID); a.State != BIOSReplaced || b.State != BIOSActive || b.Result != "conferida na BIOS" {
		t.Errorf("resolvida: %+v / %+v", a, b)
	}
	// Incerta resolvida como "a anterior vale": a nova falhou, a ativa continua.
	b3 := nova()
	_, _ = st.AttachBIOSJob(ctx, b3.ID, 70, t0)
	_, _ = st.SettleBIOSPassword(ctx, b3.ID, 70, BIOSUncertain, "x", t0)
	_, _ = st.ResolveBIOSPassword(ctx, b3.ID, false, "a anterior vale", t0)
	if a, b := estado(b2.ID), estado(b3.ID); a.State != BIOSActive || b.State != BIOSFailed {
		t.Errorf("anterior vale: %+v / %+v", a, b)
	}
}

func TestGravacaoBIOSMemoria(t *testing.T) { testarGravacaoBIOS(t, NewMemory()) }

func TestGravacaoBIOSPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarGravacaoBIOS(t, NewPostgres(pool))
}

func TestGuardaBIOSMemoria(t *testing.T) { testarGuardaBIOS(t, NewMemory()) }

func TestGuardaBIOSPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarGuardaBIOS(t, NewPostgres(pool))
}
