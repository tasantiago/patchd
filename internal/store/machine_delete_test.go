package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// A exclusão definitiva no PostgreSQL: a aposentada sai com o que é só dela (inclusive as
// tabelas sem cascata); a ativa, a com senha da BIOS guardada e a que não existe ficam.
func TestExcluirAposentadaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	ids := []string{"fantasma-0001", "ativa-0002", "bios-0003"}
	for _, id := range ids {
		_, ch := identity.NewSecret(identity.CredentialPrefix)
		if _, err := st.Enroll(ctx, identity.Hash(tok), identity.NewMachine{ID: id, CredentialHash: ch, Keys: identity.Keys{InstallID: "guid-" + id},
			Evidence: protocol.IdentityEvidence{OSFamily: "windows"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, CollectedAt: time.Now().UTC(),
			OS: protocol.OSInfo{Hostname: "pc-" + id, Name: "Windows 11 Pro"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Uma amostra de desempenho (tabela sem cascata) na que vai sair.
	if _, err := pool.Exec(ctx, `INSERT INTO perf_samples (machine_id, at, cpu, mem) VALUES ($1, now(), 10, 20)`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateBIOSPassword(ctx, BIOSPassword{MachineID: ids[2], Escrow: []byte{7}, EscrowKeyID: "0123456789ab", CreatedAt: time.Now(), CreatedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if _, err := st.RetireMachine(ctx, id, "teste", "thyago"); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		id   string
		want error
	}{{ids[1], ErrNotRetired}, {ids[2], ErrKeepsBIOS}, {"nao-existe", ErrMachineNotFound}, {ids[0], nil}} {
		if err := st.DeleteRetiredMachine(ctx, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: %v; esperava %v", c.id, err, c.want)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM machines WHERE id = $1) + (SELECT count(*) FROM perf_samples WHERE machine_id = $1)
		+ (SELECT count(*) FROM inventory_reports WHERE machine_id = $1)`, ids[0]).Scan(&n); err != nil || n != 0 {
		t.Errorf("sobrou da excluída: %d %v", n, err)
	}
	ret, _ := st.RetiredMachines(ctx)
	if len(ret) != 1 || ret[0].ID != ids[2] {
		t.Errorf("aposentadas: %+v", ret)
	}
}
