package store

import (
	"context"
	"testing"
)

type contratoTentarAberto interface {
	TryOpenSettings(ctx context.Context) (map[string]bool, error)
	SaveTryOpen(ctx context.Context, s TryOpenSetting) error
}

// Tentar com o programa aberto: grava, troca (ligado e desligado ficam) e recusa o fora do formato.
func testarTentarAberto(t *testing.T, st contratoTentarAberto) {
	ctx := context.Background()
	for _, s := range []TryOpenSetting{{AppID: "google-chrome", Enabled: true, UpdatedBy: "ana"}, {AppID: "7zip", Enabled: true, UpdatedBy: "ana"},
		{AppID: "7zip", Enabled: false, UpdatedBy: "bia"}} {
		if err := st.SaveTryOpen(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	m, err := st.TryOpenSettings(ctx)
	if err != nil || len(m) != 2 || !m["google-chrome"] || m["7zip"] {
		t.Fatalf("escolhas: %v %v", m, err)
	}
	for _, ruim := range []TryOpenSetting{{UpdatedBy: "a"}, {AppID: "x"}} {
		if err := st.SaveTryOpen(ctx, ruim); err == nil {
			t.Errorf("aceitou %+v", ruim)
		}
	}
}

func TestTentarAbertoMemoria(t *testing.T) { testarTentarAberto(t, NewMemory()) }

func TestTentarAbertoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarTentarAberto(t, NewPostgres(pool))
}
