package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoAposentadoria interface {
	CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error)
	Enroll(ctx context.Context, tokenHash []byte, m identity.NewMachine) ([]protocol.IdentityLink, error)
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	RetiredMachines(ctx context.Context) ([]RetiredMachine, error)
	MachineByCredential(ctx context.Context, credentialHash []byte) (string, bool, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	RestoreMachine(ctx context.Context, id string) (bool, error)
}

func testarAposentadoria(t *testing.T, st contratoAposentadoria) {
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	cred := map[string][]byte{}
	for i, id := range []string{"antiga-0001", "atual-0002"} {
		_, ch := identity.NewSecret(identity.CredentialPrefix)
		cred[id] = ch
		k := identity.Keys{InstallID: "guid-" + id}
		if _, err := st.Enroll(ctx, identity.Hash(tok), identity.NewMachine{ID: id, CredentialHash: ch, Keys: k,
			Evidence: protocol.IdentityEvidence{OSFamily: "linux"}}); err != nil {
			t.Fatalf("registro %d: %v", i, err)
		}
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, CollectedAt: time.Now().UTC(),
			OS: protocol.OSInfo{Hostname: "vm-lab", Name: "Ubuntu", Version: "26.04"}}); err != nil {
			t.Fatal(err)
		}
	}

	if ok, err := st.RetireMachine(ctx, "antiga-0001", "registro antigo da VM reinstalada", "thyago"); !ok || err != nil {
		t.Fatalf("aposentar: %t %v", ok, err)
	}
	if ok, _ := st.RetireMachine(ctx, "antiga-0001", "de novo", "x"); ok {
		t.Error("aposentar de novo não muda nada")
	}
	if ok, _ := st.RetireMachine(ctx, "nao-existe", "x", "x"); ok {
		t.Error("máquina inexistente")
	}

	lista, _ := st.Machines(ctx)
	if len(lista) != 1 || lista[0].ID != "atual-0002" {
		t.Errorf("a aposentada sai da frota: %+v", lista)
	}
	ap, err := st.RetiredMachines(ctx)
	if err != nil || len(ap) != 1 || ap[0].ID != "antiga-0001" || ap[0].Reason != "registro antigo da VM reinstalada" ||
		ap[0].RetiredAt.IsZero() || ap[0].Hostname != "vm-lab" || ap[0].RetiredBy != "thyago" {
		t.Errorf("aposentadas: %+v %v", ap, err)
	}
	if _, found, _ := st.MachineByCredential(ctx, cred["antiga-0001"]); found {
		t.Error("a credencial da aposentada não vale")
	}
	if id, found, _ := st.MachineByCredential(ctx, cred["atual-0002"]); !found || id != "atual-0002" {
		t.Error("a da ativa continua valendo")
	}

	if ok, err := st.RestoreMachine(ctx, "antiga-0001"); !ok || err != nil {
		t.Fatalf("restaurar: %t %v", ok, err)
	}
	if ok, _ := st.RestoreMachine(ctx, "antiga-0001"); ok {
		t.Error("restaurar a que já está na frota não muda nada")
	}
	if _, found, _ := st.MachineByCredential(ctx, cred["antiga-0001"]); !found {
		t.Error("restaurada, a credencial volta a valer")
	}
	if lista, _ := st.Machines(ctx); len(lista) != 2 {
		t.Errorf("de volta à frota: %d", len(lista))
	}
	if ap, _ := st.RetiredMachines(ctx); len(ap) != 0 {
		t.Errorf("sem aposentadas: %+v", ap)
	}
}

func TestAposentadoriaMemoria(t *testing.T) { testarAposentadoria(t, NewMemory()) }

func TestAposentadoriaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAposentadoria(t, NewPostgres(pool))
}
