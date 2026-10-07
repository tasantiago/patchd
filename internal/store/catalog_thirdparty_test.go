package store

import (
	"context"
	"testing"
	"time"
)

func TestCatalogoTerceiros(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	for _, v := range [][4]string{
		{"google-chrome", "windows", "154.0.8037.93", "chrome:win64"},
		{"google-chrome", "macos", "155.0.8059.40", "chrome:mac"},
		{"7zip", "windows", "26.04", "winget:7zip.7zip"},
		{"google-chrome", "windows", "155.0.8059.40", "chrome:win64"}, // a mesma regra de novo: atualiza
	} {
		if err := st.SaveThirdPartyVersion(ctx, v[0], v[1], v[2], v[3]); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := st.ThirdPartyVersions(ctx)
	if err != nil || len(refs) != 3 {
		t.Fatalf("referências: %+v %v", refs, err)
	}
	for _, r := range refs {
		if r.App == "google-chrome" && r.OS == "windows" && (r.Version != "155.0.8059.40" || time.Since(r.Fetched) > time.Minute) {
			t.Errorf("Chrome atualizado: %+v", r)
		}
	}

	// A regra do 7-Zip saiu do manifesto: a versão dela some.
	n, err := st.PruneThirdPartyVersions(ctx, []string{"google-chrome/windows", "google-chrome/macos"})
	if err != nil || n != 1 {
		t.Errorf("limpeza: %d %v", n, err)
	}
	if refs, _ = st.ThirdPartyVersions(ctx); len(refs) != 2 || refs[0].OS != "macos" {
		t.Errorf("depois da limpeza: %+v", refs)
	}
}
