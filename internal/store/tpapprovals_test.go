package store

import (
	"context"
	"testing"
	"time"
)

type contratoAprovacaoTerceiros interface {
	ThirdPartyApprovals(ctx context.Context) ([]ThirdPartyApproval, error)
	ApproveThirdParty(ctx context.Context, a ThirdPartyApproval) (bool, error)
	RevokeThirdParty(ctx context.Context, app, version string) (bool, error)
}

func testarAprovacaoTerceiros(t *testing.T, st contratoAprovacaoTerceiros) {
	ctx := context.Background()
	if l, err := st.ThirdPartyApprovals(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	if ok, err := st.ApproveThirdParty(ctx, ThirdPartyApproval{AppID: "notepad-plus-plus", Version: "8.9.8.1", ApprovedBy: "ana", Note: "piloto ok"}); err != nil || !ok {
		t.Fatalf("aprovar: %t %v", ok, err)
	}
	if ok, _ := st.ApproveThirdParty(ctx, ThirdPartyApproval{AppID: "notepad-plus-plus", Version: "8.9.8.1", ApprovedBy: "bruno"}); ok {
		t.Error("a segunda aprovação não troca a primeira")
	}
	time.Sleep(10 * time.Millisecond)
	if ok, _ := st.ApproveThirdParty(ctx, ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "bruno"}); !ok {
		t.Error("7zip")
	}
	l, _ := st.ThirdPartyApprovals(ctx)
	if len(l) != 2 || l[0].AppID != "7zip" || l[1].ApprovedBy != "ana" || l[1].Note != "piloto ok" || l[1].ApprovedAt.IsZero() {
		t.Fatalf("lista: %+v", l)
	}
	if ok, err := st.RevokeThirdParty(ctx, "7zip", "26.04"); err != nil || !ok {
		t.Errorf("revogar: %t %v", ok, err)
	}
	if ok, _ := st.RevokeThirdParty(ctx, "7zip", "26.04"); ok {
		t.Error("revogar de novo")
	}
	if l, _ := st.ThirdPartyApprovals(ctx); len(l) != 1 {
		t.Errorf("depois de revogar: %+v", l)
	}
}

func TestAprovacaoTerceirosMemoria(t *testing.T) { testarAprovacaoTerceiros(t, NewMemory()) }

func TestAprovacaoTerceirosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAprovacaoTerceiros(t, NewPostgres(pool))
}
