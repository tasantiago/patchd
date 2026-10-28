package store

import (
	"context"
	"errors"
	"testing"
)

type contratoPatrimonio interface {
	ReplaceAssetLocations(ctx context.Context, list []AssetLocation, imp AssetImport) error
	AssetLocations(ctx context.Context) ([]AssetLocation, error)
	AssetImports(ctx context.Context, limit int) ([]AssetImport, error)
}

// Aula 23.6: cada importação substitui a anterior inteira; vazia não apaga nada.
func testarPatrimonio(t *testing.T, st contratoPatrimonio) {
	ctx := context.Background()
	if l, err := st.AssetLocations(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	a := []AssetLocation{{Tombo: "102874", Building: "FORUM EXEMPLO", Room: "SALA 101"}, {Tombo: "9999", Building: "ANEXO"},
		{Tombo: "88594", Room: "SALA 102", Unit: "UOX", Description: "DESKTOP", Subgroup: "COMPUTADOR"}}
	if err := st.ReplaceAssetLocations(ctx, a, AssetImport{By: "thyago", File: "a.csv", Skipped: 2, Duplicates: 1}); err != nil {
		t.Fatal(err)
	}
	l, err := st.AssetLocations(ctx)
	if err != nil || len(l) != 3 || l[0].Tombo != "9999" || l[1] != a[2] || l[2].Tombo != "102874" {
		t.Fatalf("lista (pelo tombo, como número): %+v %v", l, err)
	}
	if err := st.ReplaceAssetLocations(ctx, nil, AssetImport{By: "thyago"}); !errors.Is(err, ErrNoAssets) {
		t.Fatalf("vazia: %v", err)
	}
	if err := st.ReplaceAssetLocations(ctx, a[:1], AssetImport{By: "ana", File: "b.csv"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.AssetLocations(ctx); len(l) != 1 || l[0].Tombo != "102874" {
		t.Errorf("substituída: %+v", l)
	}
	imps, err := st.AssetImports(ctx, 5)
	if err != nil || len(imps) != 2 || imps[0].File != "b.csv" || imps[0].Kept != 1 || imps[1].Kept != 3 || imps[1].Skipped != 2 ||
		imps[1].Duplicates != 1 || imps[0].At.IsZero() {
		t.Errorf("importações: %+v %v", imps, err)
	}
}

func TestPatrimonioMemoria(t *testing.T) { testarPatrimonio(t, NewMemory()) }

func TestPatrimonioPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarPatrimonio(t, NewPostgres(pool))
}
