package store

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/apple"
)

func TestComplianceMacOSConsultas(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	abrir := func(nome string) *os.File {
		f, err := os.Open("../catalog/apple/testdata/" + nome)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	vs, err := apple.ParseGDMF(abrir("gdmf-pmv-recorte.json"))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := apple.ParseSOFA(abrir("sofa-macos-v2-recorte.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Um modelo com sufixo, como as entradas "MacPro7,1-Rack" do SOFA.
	feed.Models = append(feed.Models, apple.Model{ID: "MacPro7,1-Rack", MarketingName: "Mac Pro (Rack, 2019)", Majors: []int{26, 15}})
	if err := st.SaveAppleVersions(ctx, vs, "g"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSOFA(ctx, feed); err != nil {
		t.Fatal(err)
	}

	// Majors: o recorte do 26 não tem o 26.0, então o "lançamento" é a versão mais antiga.
	ms, err := st.MacOSMajors(ctx)
	if err != nil || len(ms) != 4 {
		t.Fatalf("majors: %+v %v", ms, err)
	}
	dia := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	if m := ms[0]; m.Number != 27 || m.Name != "Golden Gate 27" || !m.Launched.Equal(dia("2026-09-14")) || m.Latest != "27.0.1" || !m.LatestDate.Equal(dia("2026-09-28")) {
		t.Errorf("27: %+v", m)
	}
	if m := ms[1]; m.Number != 26 || !m.Launched.Equal(dia("2026-08-17")) || m.Latest != "26.7.1" {
		t.Errorf("26: %+v", m)
	}
	if m := ms[2]; m.Number != 14 || m.Latest != "14.8.9" || !m.LatestDate.Equal(dia("2026-08-06")) {
		t.Errorf("14: %+v", m)
	}

	// Correções do 26: as CVEs das três versões do recorte, com a explorada no KEV.
	fx, err := st.MacOSFixes(ctx, 26)
	if err != nil || len(fx) == 0 {
		t.Fatalf("correções do 26: %+v %v", fx, err)
	}
	achou := false
	for _, f := range fx {
		if f.Version[:2] != "26" {
			t.Errorf("correção de outra major: %+v", f)
		}
		if f.CVE == "CVE-2026-86950" {
			achou = f.Version == "26.7.1" && f.Exploited && f.KEV && f.Released.Equal(dia("2026-09-28"))
		}
	}
	if !achou {
		t.Errorf("CVE-2026-86950 no 26.7.1: %+v", fx)
	}

	// Sonoma encerrado em 06/08: a explorada de 28/09 do 26.7.1 não tem correção no 14; a
	// CVE-2026-65400, também explorada, foi corrigida no 14.8.9 e não aparece.
	sem, err := st.MacOSUnfixed(ctx, 14, dia("2026-08-06"))
	if err != nil || len(sem) != 1 || sem[0].CVE != "CVE-2026-86950" || !sem[0].KEV || !slices.Equal(sem[0].Elsewhere, []string{"26.7.1"}) {
		t.Errorf("sem correção no 14: %+v %v", sem, err)
	}

	// Modelos: exato, pelo identificador antes do hífen, e desconhecido.
	if m, ok, err := st.AppleModel(ctx, "MacBookAir8,1"); err != nil || !ok || !slices.Equal(m.Majors, []int{14, 13, 12}) {
		t.Errorf("MacBookAir8,1: %+v %t %v", m, ok, err)
	}
	if m, ok, err := st.AppleModel(ctx, "MacPro7,1"); err != nil || !ok || m.ID != "MacPro7,1-Rack" {
		t.Errorf("MacPro7,1: %+v %t %v", m, ok, err)
	}
	if _, ok, err := st.AppleModel(ctx, "Mac99,9"); ok || err != nil {
		t.Errorf("modelo desconhecido: %t %v", ok, err)
	}

	// Feed sem a seção Models: os modelos guardados ficam.
	feed.Models, feed.UpdateHash = nil, "outro"
	if err := st.SaveSOFA(ctx, feed); err != nil {
		t.Fatal(err)
	}
	if n, err := st.AppleModelsCount(ctx); err != nil || n != 8 {
		t.Errorf("modelos depois de um feed sem a seção: %d %v", n, err)
	}

	// A melhoria (a) reconhecida pelo build; um build normal não é melhoria.
	if x, ok, err := st.AppleExtraBuild(ctx, "25D771280a"); err != nil || !ok || x.Version != "26.3.1" || x.Extra != "(a)" || x.Prerequisite != "25D2128" {
		t.Errorf("26.3.1 (a): %+v %t %v", x, ok, err)
	}
	if _, ok, err := st.AppleExtraBuild(ctx, "25G241"); ok || err != nil {
		t.Errorf("build normal: %t %v", ok, err)
	}
}
