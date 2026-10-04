package store

import (
	"context"
	"errors"
	"fmt"
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
	Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) ([]protocol.IdentityLink, error)
	IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error)
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
	if _, err := st.Enroll(ctx, tokHash, m1); err != nil {
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
	if _, err := st.Enroll(ctx, tokHash, m2); err != nil {
		t.Fatalf("segundo registro (limite 2): %v", err)
	}
	m3, _ := novaMaquina()
	if _, err := st.Enroll(ctx, tokHash, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("terceiro registro deveria ser recusado (esgotado): %v", err)
	}
	if _, err := st.Enroll(ctx, identity.Hash("patchd_enr_inventado"), m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("token desconhecido deveria ser recusado: %v", err)
	}

	_, venc := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, venc, "vencido", time.Now().Add(-time.Minute), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enroll(ctx, venc, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Errorf("token vencido deveria ser recusado: %v", err)
	}

	_, rev := identity.NewSecret(identity.EnrollmentPrefix)
	revID, _ := st.CreateEnrollmentToken(ctx, rev, "a revogar", time.Now().Add(time.Hour), 5)
	if ok, err := st.RevokeEnrollmentToken(ctx, revID); !ok || err != nil {
		t.Fatalf("revogação: %t %v", ok, err)
	}
	if _, err := st.Enroll(ctx, rev, m3); !errors.Is(err, identity.ErrEnrollmentRejected) {
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
			_, err := st.Enroll(ctx, h, m)
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

// comChaves devolve uma máquina nova com as evidências dadas (cruas), já normalizadas.
func comChaves(installID, uuid, serial string) identity.NewMachine {
	m, _ := novaMaquina()
	m.Evidence.InstallID, m.Evidence.HardwareUUID, m.Evidence.Serial = installID, uuid, serial
	m.Keys, _ = identity.Normalize(m.Evidence)
	return m
}

func testarAlertas(t *testing.T, st contratoEnrollment) {
	ctx := context.Background()
	_, h := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, h, "alertas", time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	enroll := func(m identity.NewMachine) []protocol.IdentityLink {
		t.Helper()
		links, err := st.Enroll(ctx, h, m)
		if err != nil {
			t.Fatal(err)
		}
		return links
	}
	rel := func(links []protocol.IdentityLink) map[string]string {
		out := map[string]string{}
		for _, l := range links {
			out[l.RelatedMachineID] = l.Relation
		}
		return out
	}

	const uuid = "4c4c4544-0042-3510-8051-b7c04f4b4e32"
	const uuidInvertido = "44454c4c-4200-1035-8051-b7c04f4b4e32" // os mesmos bytes, lidos na outra ordem
	a := comChaves("{3F2504E0-4F89-11D3-9A0C-0305E82C3301}", uuid, "PF3ABC12")
	if links := enroll(a); len(links) != 0 {
		t.Fatalf("primeira máquina não tem com quem se relacionar: %+v", links)
	}

	// Agente reinstalado: as mesmas evidências (o MachineGuid em minúsculas, sem chaves).
	b := comChaves("3f2504e0-4f89-11d3-9a0c-0305e82c3301", uuid, "pf3abc12")
	if r := rel(enroll(b)); len(r) != 1 || r[a.ID] != identity.RelationReenrollment {
		t.Errorf("reinstalação do agente: %v", r)
	}

	// Clone: a mesma instalação em outro hardware. Relaciona-se com A e com B.
	c := comChaves("3f2504e0-4f89-11d3-9a0c-0305e82c3301", "11111111-2222-3333-4444-555555555555", "XYZ98765")
	if r := rel(enroll(c)); len(r) != 2 || r[a.ID] != identity.RelationClone || r[b.ID] != identity.RelationClone {
		t.Errorf("clone de imagem: %v", r)
	}

	// SO reinstalado no hardware de A, com o UUID lido na outra ordem de bytes (Linux × Windows).
	d := comChaves("4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b", uuidInvertido, "")
	if r := rel(enroll(d)); len(r) != 2 || r[a.ID] != identity.RelationSameHardware || r[b.ID] != identity.RelationSameHardware {
		t.Errorf("formatação no mesmo hardware: %v", r)
	}

	// Placa genérica: UUID e serial de fábrica não ligam máquinas diferentes entre si.
	e1 := comChaves("aaaa0000000000000000000000000001", "03000200-0400-0500-0006-000700080009", "To be filled by O.E.M.")
	e2 := comChaves("aaaa0000000000000000000000000002", "03000200-0400-0500-0006-000700080009", "To be filled by O.E.M.")
	enroll(e1)
	if links := enroll(e2); len(links) != 0 {
		t.Errorf("evidências genéricas não podem gerar alerta: %+v", links)
	}

	todos, err := st.IdentityLinks(ctx)
	if err != nil || len(todos) != 5 {
		t.Fatalf("esperados 5 alertas no total: %d %v", len(todos), err)
	}
	if todos[0].MachineID != b.ID || todos[0].CreatedAt.IsZero() {
		t.Errorf("ordem de criação e data: %+v", todos[0])
	}
}

func TestAlertasMemoria(t *testing.T) { testarAlertas(t, NewMemory()) }

func TestAlertasPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAlertas(t, NewPostgres(pool))
}

func TestClonesSimultaneosSeEnxergam(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	// Dez máquinas clonadas da mesma imagem, cada uma com um token diferente, registrando-se
	// ao mesmo tempo. Cada nova precisa enxergar as anteriores: 10×9/2 = 45 alertas.
	const n = 10
	var wg sync.WaitGroup
	for i := range n {
		_, h := identity.NewSecret(identity.EnrollmentPrefix)
		if _, err := st.CreateEnrollmentToken(ctx, h, "clone", time.Now().Add(time.Hour), 1); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			uuid := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i+1)
			if _, err := st.Enroll(ctx, h, comChaves("3f2504e0-4f89-11d3-9a0c-0305e82c3301", uuid, "")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	links, _ := st.IdentityLinks(ctx)
	if len(links) != n*(n-1)/2 {
		t.Errorf("esperados %d alertas de clone, vieram %d", n*(n-1)/2, len(links))
	}
}
