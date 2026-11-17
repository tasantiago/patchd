package store

import (
	"context"
	"encoding/json"
	"testing"
)

type contratoProgramas interface {
	CustomApps(ctx context.Context) ([]CustomApp, error)
	SaveCustomApp(ctx context.Context, a CustomApp) error
	DeleteCustomApp(ctx context.Context, id string) (bool, error)
}

// Os programas incluídos pelo painel: grava, troca (o autor da inclusão fica), lista por id e
// tira; o que não é uma regra em JSON é recusado.
func testarProgramas(t *testing.T, st contratoProgramas) {
	ctx := context.Background()
	if err := st.SaveCustomApp(ctx, CustomApp{ID: "videolan-vlc", Rule: json.RawMessage(`{"id":"videolan-vlc","name":"VLC"}`), UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCustomApp(ctx, CustomApp{ID: "gimp-gimp", Rule: json.RawMessage(`{"id":"gimp-gimp"}`), UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCustomApp(ctx, CustomApp{ID: "videolan-vlc", Rule: json.RawMessage(`{"id":"videolan-vlc","name":"VLC 2"}`), UpdatedBy: "bia"}); err != nil {
		t.Fatal(err)
	}
	l, err := st.CustomApps(ctx)
	if err != nil || len(l) != 2 || l[0].ID != "gimp-gimp" || l[1].CreatedBy != "ana" || l[1].UpdatedBy != "bia" || l[1].UpdatedAt.IsZero() {
		t.Fatalf("lista: %+v %v", l, err)
	}
	var r map[string]string
	if json.Unmarshal(l[1].Rule, &r) != nil || r["name"] != "VLC 2" {
		t.Errorf("troca: %s", l[1].Rule)
	}
	for _, ruim := range []CustomApp{{Rule: json.RawMessage(`{}`), UpdatedBy: "a"}, {ID: "x", Rule: json.RawMessage(`[1]`), UpdatedBy: "a"},
		{ID: "x", Rule: json.RawMessage(`{}`)}} {
		if err := st.SaveCustomApp(ctx, ruim); err == nil {
			t.Errorf("aceito: %+v", ruim)
		}
	}
	if ok, _ := st.DeleteCustomApp(ctx, "gimp-gimp"); !ok {
		t.Error("excluir")
	}
	if ok, _ := st.DeleteCustomApp(ctx, "gimp-gimp"); ok {
		t.Error("excluir de novo")
	}
	if l, _ := st.CustomApps(ctx); len(l) != 1 {
		t.Errorf("depois de excluir: %+v", l)
	}
}

func TestProgramasMemoria(t *testing.T) { testarProgramas(t, NewMemory()) }

func TestProgramasPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarProgramas(t, NewPostgres(pool))
}
