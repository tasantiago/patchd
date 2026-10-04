package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoEnrollment interface {
	CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error)
	EnrollmentTokens(ctx context.Context) ([]identity.EnrollmentToken, error)
	RevokeEnrollmentToken(ctx context.Context, id int64) (bool, error)
	Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) error
	MachineByCredential(ctx context.Context, credentialHash []byte) (string, bool, error)
}

func novaMaquina() (identity.NewMachine, string) {
	cred, hash := identity.NewSecret(identity.CredentialPrefix)
	return identity.NewMachine{
		ID:             identity.NewMachineID(),
		CredentialHash: hash,
		AgentVersion:   "v0.4.0-teste",
		Evidence:       protocol.IdentityEvidence{InstallID: "guid-1", Hostname: "FJ106260", OSFamily: "windows"},
	}, cred
}

func testarEnrollment(t *testing.T, st contratoEnrollment) {
	ctx := context.Background()
	_, tokHash := identity.NewSecret(identity.EnrollmentPrefix)
	tokID, err := st.CreateEnrollmentToken(ctx, tokHash, "piloto", time.Now().Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}

	m1, cred1 := novaMaquina()
	if err := st.Enroll(ctx, tokHash, m1); err != nil {
		t.Fatalf("primeiro registro: %v", err)
	}
	id, ok, err := st.MachineByCredential(ctx, identity.Hash(cred1))
	if err != nil || !ok || id != m1.ID {
		t.Fatalf("a credencial deveria levar à máquina: %q %t %v", id, ok, err)
	}
	if _, ok, _ := st.MachineByCredential(ctx, identity.Hash("patchd_mac_inventada")); ok {
		t.Error("credencial desconhecida não pode levar a máquina nenhuma")
	}

	m2, _ := novaMaquina()
	if err := st.Enroll(ctx, tokHash, m2); err != nil {
		t.Fatalf("segundo registro (limite 2): %v", err)
	}
	m3, _ := novaMaquina()
	if err := st.Enroll(ctx, tokHash, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("terceiro registro deveria ser recusado (esgotado): %v", err)
	}
	if err := st.Enroll(ctx, identity.Hash("patchd_enr_inventado"), m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("token desconhecido deveria ser recusado: %v", err)
	}

	_, venc := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, venc, "vencido", time.Now().Add(-time.Minute), 5); err != nil {
		t.Fatal(err)
	}
	if err := st.Enroll(ctx, venc, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("token vencido deveria ser recusado: %v", err)
	}

	_, rev := identity.NewSecret(identity.EnrollmentPrefix)
	revID, _ := st.CreateEnrollmentToken(ctx, rev, "a revogar", time.Now().Add(time.Hour), 5)
	if ok, err := st.RevokeEnrollmentToken(ctx, revID); !ok || err != nil {
		t.Fatalf("revogação: %t %v", ok, err)
	}
	if err := st.Enroll(ctx, rev, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("token revogado deveria ser recusado: %v", err)
	}
	if ok, _ := st.RevokeEnrollmentToken(ctx, 999999); ok {
		t.Error("revogar ID inexistente deveria devolver false")
	}

	lista, err := st.EnrollmentTokens(ctx)
	if err != nil || len(lista) != 3 || lista[2].ID != tokID || lista[2].Uses != 2 || lista[2].Note != "piloto" {
		t.Fatalf("lista de tokens (mais novo primeiro): %+v %v", lista, err)
	}
	agora := time.Now()
	if lista[0].Status(agora) != "revogado" || lista[1].Status(agora) != "expirado" || lista[2].Status(agora) != "esgotado" {
		t.Errorf("situações erradas: %s %s %s", lista[0].Status(agora), lista[1].Status(agora), lista[2].Status(agora))
	}
}

func TestEnrollmentMemoria(t *testing.T) { testarEnrollment(t, NewMemory()) }

func TestEnrollmentPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarEnrollment(t, NewPostgres(pool))
}

func TestEnrollmentSimultaneoRespeitaLimite(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	_, h := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, h, "limite 3", time.Now().Add(time.Hour), 3); err != nil {
		t.Fatal(err)
	}

	// Vinte instalações disparadas juntas (um deploy em massa) contra um limite de 3.
	var wg sync.WaitGroup
	var mu sync.Mutex
	aceitos := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _ := novaMaquina()
			err := st.Enroll(ctx, h, m)
			if err != nil && !errors.Is(err, identity.ErrEnrollmentRejected) {
				t.Error(err)
			}
			if err == nil {
				mu.Lock()
				aceitos++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if aceitos != 3 {
		t.Errorf("o limite é 3; foram aceitos %d", aceitos)
	}
}
