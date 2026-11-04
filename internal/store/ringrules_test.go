package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type contratoRegrasDeGrupo interface {
	IPRings(ctx context.Context) ([]IPRing, error)
	AddIPRing(ctx context.Context, r IPRing) (int64, error)
	DeleteIPRing(ctx context.Context, id int64) (IPRing, bool, error)
	RingNames(ctx context.Context) ([]RingName, error)
	SetRingName(ctx context.Context, n RingName) error
}

func testarRegrasDeGrupo(t *testing.T, st contratoRegrasDeGrupo) {
	ctx := context.Background()
	id, err := st.AddIPRing(ctx, IPRing{Spec: "10.1.2.0/24", Ring: 3, UpdatedBy: "ana"})
	if err != nil || id == 0 {
		t.Fatalf("faixa: %d %v", id, err)
	}
	if _, err := st.AddIPRing(ctx, IPRing{Spec: "10.1.2.0/24", Ring: 4, UpdatedBy: "bia"}); !errors.Is(err, ErrIPSpecTaken) {
		t.Errorf("repetida: %v", err)
	}
	if _, err := st.AddIPRing(ctx, IPRing{Spec: strings.Repeat("1", 65), Ring: 1}); !errors.Is(err, ErrIPSpec) {
		t.Errorf("longa: %v", err)
	}
	if _, err := st.AddIPRing(ctx, IPRing{Spec: "10.9.*", Ring: 10}); !errors.Is(err, ErrRing) {
		t.Errorf("grupo 10: %v", err)
	}
	id2, _ := st.AddIPRing(ctx, IPRing{Spec: "10.9.*", Ring: 5, UpdatedBy: "ana"})
	if l, _ := st.IPRings(ctx); len(l) != 2 || l[0].Spec != "10.1.2.0/24" || l[1].ID != id2 || l[0].UpdatedAt.IsZero() {
		t.Errorf("lista: %+v", l)
	}
	if r, ok, err := st.DeleteIPRing(ctx, id); !ok || err != nil || r.Spec != "10.1.2.0/24" || r.Ring != 3 {
		t.Errorf("remover: %+v %v %v", r, ok, err)
	}
	if _, ok, _ := st.DeleteIPRing(ctx, id); ok {
		t.Error("remover de novo")
	}

	if err := st.SetRingName(ctx, RingName{Ring: 0, Name: "Piloto TI", UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRingName(ctx, RingName{Ring: 0, Name: "Piloto", UpdatedBy: "bia"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRingName(ctx, RingName{Ring: 3, Name: strings.Repeat("é", 41)}); !errors.Is(err, ErrRingName) {
		t.Errorf("nome longo: %v", err)
	}
	if n, _ := st.RingNames(ctx); len(n) != 1 || n[0].Name != "Piloto" || n[0].UpdatedBy != "bia" {
		t.Errorf("nomes: %+v", n)
	}
	if err := st.SetRingName(ctx, RingName{Ring: 0, Name: ""}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.RingNames(ctx); len(n) != 0 {
		t.Errorf("apagar nome: %+v", n)
	}
}

func TestRegrasDeGrupoMemoria(t *testing.T) { testarRegrasDeGrupo(t, NewMemory()) }

func TestRegrasDeGrupoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarRegrasDeGrupo(t, NewPostgres(pool))
}
