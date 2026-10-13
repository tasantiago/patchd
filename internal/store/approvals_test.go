package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoAprovacao interface {
	PatchApprovals(ctx context.Context) ([]PatchApproval, error)
	ApprovePatch(ctx context.Context, a PatchApproval) (bool, error)
	ApprovePatches(ctx context.Context, list []PatchApproval) ([]PatchApproval, error)
	RevokePatch(ctx context.Context, item, fixedIn string) (bool, error)
}

func testarAprovacao(t *testing.T, st contratoAprovacao) {
	ctx := context.Background()
	if l, err := st.PatchApprovals(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	ok, err := st.ApprovePatch(ctx, PatchApproval{Item: "curl", FixedIn: "8.18.0-1ubuntu2.10", ApprovedBy: "ana", Note: "testado no piloto"})
	if err != nil || !ok {
		t.Fatalf("aprovar: %t %v", ok, err)
	}
	// A mesma correção de novo: a aprovação original fica.
	if ok, err := st.ApprovePatch(ctx, PatchApproval{Item: "curl", FixedIn: "8.18.0-1ubuntu2.10", ApprovedBy: "bruno"}); err != nil || ok {
		t.Errorf("segunda aprovação: %t %v", ok, err)
	}
	time.Sleep(10 * time.Millisecond)
	// Outra correção do mesmo item é outra aprovação.
	if ok, _ := st.ApprovePatch(ctx, PatchApproval{Item: "curl", FixedIn: "8.18.0-1ubuntu2.11", ApprovedBy: "bruno"}); !ok {
		t.Error("outra correção")
	}
	l, _ := st.PatchApprovals(ctx)
	if len(l) != 2 || l[0].FixedIn != "8.18.0-1ubuntu2.11" || l[1].ApprovedBy != "ana" || l[1].Note != "testado no piloto" || l[1].ApprovedAt.IsZero() {
		t.Fatalf("lista (mais recente primeiro): %+v", l)
	}
	if ok, err := st.RevokePatch(ctx, "curl", "8.18.0-1ubuntu2.10"); err != nil || !ok {
		t.Errorf("revogar: %t %v", ok, err)
	}
	if ok, _ := st.RevokePatch(ctx, "curl", "8.18.0-1ubuntu2.10"); ok {
		t.Error("revogar de novo: nada a revogar")
	}
	if l, _ := st.PatchApprovals(ctx); len(l) != 1 {
		t.Errorf("depois de revogar: %+v", l)
	}

	// Lote (Aula 13.4): os novos entram; o que já existia não conta nem muda.
	novas, err := st.ApprovePatches(ctx, []PatchApproval{
		{Item: "curl", FixedIn: "8.18.0-1ubuntu2.11", ApprovedBy: "carla"}, // já aprovado por bruno
		{Item: "bluez", FixedIn: "5.80-1ubuntu0.2", ApprovedBy: "carla", Note: "lote de outubro"},
		{Item: "CVE-2026-1", FixedIn: "26200.7019 (KB5070773)", ApprovedBy: "carla", Note: "lote de outubro"},
	})
	if err != nil || len(novas) != 2 || novas[0].Item != "bluez" || novas[1].Item != "CVE-2026-1" {
		t.Fatalf("lote: %+v %v", novas, err)
	}
	l, _ = st.PatchApprovals(ctx)
	quem := map[string]string{}
	for _, a := range l {
		quem[a.Item] = a.ApprovedBy
	}
	if len(l) != 3 || quem["curl"] != "bruno" || quem["bluez"] != "carla" {
		t.Errorf("depois do lote: %+v", l)
	}
}

func TestAprovacaoMemoria(t *testing.T) { testarAprovacao(t, NewMemory()) }

func TestAprovacaoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAprovacao(t, NewPostgres(pool))
}

func TestApenasAprovados(t *testing.T) {
	items := []protocol.PendingItem{
		{ID: "rsync", FixedIn: "3.4.1+ds1-7ubuntu0.3", Packages: []string{"rsync"}},
		{ID: "curl", FixedIn: "8.18.0-1ubuntu2.11", Packages: []string{"curl"}},
		{ID: "bzip2", FixedIn: "1.0.8-6ubuntu0.1", Packages: []string{"bzip2"}},
	}
	ap := []PatchApproval{
		{Item: "rsync", FixedIn: "3.4.1+ds1-7ubuntu0.3"},
		{Item: "curl", FixedIn: "8.18.0-1ubuntu2.10"}, // a correção anterior: não vale para a nova
	}
	got := ApprovedOnly(items, ap)
	if len(got) != 1 || got[0].ID != "rsync" {
		t.Errorf("só o rsync, com a correção aprovada: %+v", got)
	}
	if got := ApprovedOnly(items, nil); len(got) != 0 {
		t.Errorf("sem aprovação, nada: %+v", got)
	}
}

// Um item inválido no meio do lote (o banco recusa: item vazio) desfaz o lote inteiro.
func TestLoteTudoOuNadaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	_, err := st.ApprovePatches(ctx, []PatchApproval{
		{Item: "rsync", FixedIn: "3.4.1+ds1-7ubuntu0.3", ApprovedBy: "ana"},
		{Item: "", FixedIn: "x", ApprovedBy: "ana"},
	})
	if err == nil {
		t.Fatal("o item vazio devia ser recusado pelo banco")
	}
	if l, _ := st.PatchApprovals(ctx); len(l) != 0 {
		t.Errorf("nada do lote pode ficar: %+v", l)
	}
}
