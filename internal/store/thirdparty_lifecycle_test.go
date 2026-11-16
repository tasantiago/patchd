package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/thirdparty"
)

// As fontes novas dos programas de terceiros no banco: a conferência ao lado da versão
// principal, e os ciclos do endoflife.date trocados por produto, com a limpeza dos que saíram.
func TestTerceirosCiclosEConferenciaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	if err := st.SaveThirdPartyVersion(ctx, "7zip", "windows", "26.04", "winget:7zip.7zip"); err != nil {
		t.Fatal(err)
	}
	// Conferência de uma regra sem linha: nada acontece (a principal grava primeiro).
	if err := st.SaveThirdPartyCheck(ctx, "nada", "windows", "1", "evergreen:Nada"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveThirdPartyCheck(ctx, "7zip", "windows", "26.05", "evergreen:7zip"); err != nil {
		t.Fatal(err)
	}
	refs, err := st.ThirdPartyVersions(ctx)
	if err != nil || len(refs) != 1 || refs[0].CheckVersion != "26.05" || refs[0].CheckSource != "evergreen:7zip" || refs[0].Version != "26.04" {
		t.Fatalf("versões: %+v %v", refs, err)
	}
	// A principal de novo não apaga a conferência.
	if err := st.SaveThirdPartyVersion(ctx, "7zip", "windows", "26.05", "winget:7zip.7zip"); err != nil {
		t.Fatal(err)
	}
	if refs, _ = st.ThirdPartyVersions(ctx); refs[0].CheckVersion != "26.05" {
		t.Errorf("conferência perdida: %+v", refs[0])
	}

	fim := time.Date(2030, 10, 31, 0, 0, 0, 0, time.UTC)
	py := []thirdparty.Cycle{{Name: "3.14", EOLFrom: &fim, Latest: "3.14.10"}, {Name: "3.9", IsEOL: true, Latest: "3.9.25"}, {Name: "4.0"}}
	if err := st.ReplaceLifecycle(ctx, "python", py); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceLifecycle(ctx, "nodejs", []thirdparty.Cycle{{Name: "22", Latest: "22.11.0"}}); err != nil {
		t.Fatal(err)
	}
	// A troca é do produto inteiro: o ciclo que sumiu da resposta sai.
	if err := st.ReplaceLifecycle(ctx, "python", py[:2]); err != nil {
		t.Fatal(err)
	}
	cs, err := st.Lifecycles(ctx)
	if err != nil || len(cs) != 3 {
		t.Fatalf("ciclos: %+v %v", cs, err)
	}
	por := map[string]thirdparty.Cycle{}
	for _, c := range cs {
		por[c.Product+"/"+c.Name] = c
	}
	if c := por["python/3.14"]; c.EOLFrom == nil || !c.EOLFrom.Equal(fim) || c.IsEOL || c.Latest != "3.14.10" || c.Fetched.IsZero() {
		t.Errorf("3.14: %+v", c)
	}
	if c := por["python/3.9"]; !c.IsEOL || c.EOLFrom != nil {
		t.Errorf("3.9: %+v", c)
	}
	if n, err := st.PruneLifecycles(ctx, []string{"python"}); err != nil || n != 1 {
		t.Errorf("limpeza: %d %v", n, err)
	}
	if cs, _ = st.Lifecycles(ctx); len(cs) != 2 {
		t.Errorf("depois da limpeza: %+v", cs)
	}
}
