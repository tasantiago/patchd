package store

import (
	"context"
	"os"
	"testing"

	"github.com/tasantiago/patchd/internal/catalog/apple"
)

func TestCatalogoApple(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	g, err := os.Open("../catalog/apple/testdata/gdmf-pmv-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	vs, err := apple.ParseGDMF(g)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.Open("../catalog/apple/testdata/sofa-macos-v2-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	feed, err := apple.ParseSOFA(s)
	if err != nil {
		t.Fatal(err)
	}

	if h, err := st.FeedHash(ctx, FeedAppleSOFA); err != nil || h != "" {
		t.Fatalf("fonte nunca gravada: %q %v", h, err)
	}
	if err := st.SaveAppleVersions(ctx, vs, "hash-gdmf-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSOFA(ctx, feed); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.FeedHash(ctx, FeedAppleSOFA); h != feed.UpdateHash {
		t.Errorf("UpdateHash guardado: %q", h)
	}
	if h, _ := st.FeedHash(ctx, FeedAppleGDMF); h != "hash-gdmf-1" {
		t.Errorf("hash do gdmf: %q", h)
	}

	ms, err := st.AppleMajors(ctx)
	if err != nil || len(ms) != 4 {
		t.Fatalf("majors: %+v %v", ms, err)
	}
	// A mais nova primeiro (27, 26, 14, 12); o 27.0.1 tem dois builds no gdmf; as "(a)" não
	// contam como a maior versão do 26.
	if ms[0].Number != "27" || ms[0].Latest != "27.0.1" || ms[0].GDMFLatest != "27.0.1" || len(ms[0].GDMFBuilds) != 2 {
		t.Errorf("27: %+v", ms[0])
	}
	if ms[1].Number != "26" || ms[1].Latest != "26.7.1" || ms[1].GDMFLatest != "26.7.1" || ms[1].Exploited != 1 {
		t.Errorf("26: %+v", ms[1])
	}
	if ms[3].Number != "12" || ms[3].GDMFLatest != "12.7.6" {
		t.Errorf("12: %+v", ms[3])
	}

	rs, err := st.AppleReleases(ctx, "26")
	if err != nil || len(rs) != 3 || rs[0].Version != "26.7.1" || rs[1].CVEs != 153 {
		t.Errorf("versões do 26: %+v %v", rs, err)
	}

	// Substituir: um feed menor apaga o que saiu, em cascata.
	feed.Releases = feed.Releases[:1]
	feed.UpdateHash = "novo"
	if err := st.SaveSOFA(ctx, feed); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM apple_releases) + (SELECT count(*) FROM apple_release_cves WHERE product_version = '26.7.1')`).Scan(&n); err != nil || n != 1 {
		t.Errorf("substituição: %d %v", n, err)
	}
}
