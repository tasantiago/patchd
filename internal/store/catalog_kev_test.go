package store

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/apple"
	"github.com/tasantiago/patchd/internal/catalog/bodhi"
	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

func TestCatalogoKEV(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	f, err := os.Open("../catalog/kev/testdata/kev-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cat, err := kev.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveKEV(ctx, cat); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.FeedHash(ctx, FeedKEV); h != cat.Released {
		t.Errorf("dateReleased guardado: %q", h)
	}

	// MSRC: uma CVE explorada pela Microsoft e no KEV; outra no KEV que a Microsoft não marcou.
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	doc := msrc.Document{ID: "2026-Sep", Title: "Setembro", Initial: set, Current: set,
		Vulns: []msrc.Vuln{{CVE: "CVE-2026-81963", Exploited: true}, {CVE: "CVE-2026-65660"}, {CVE: "CVE-2026-0001"}}}
	if err := st.SaveMSRCDocument(ctx, doc, set); err != nil {
		t.Fatal(err)
	}
	// Apple: o recorte do SOFA tem a CVE-2026-86950 no 26.7.1.
	sf, err := os.Open("../catalog/apple/testdata/sofa-macos-v2-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	feed, err := apple.ParseSOFA(sf)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSOFA(ctx, feed); err != nil {
		t.Fatal(err)
	}
	// Fedora: o chromium real (cita CVEs, mas não a do KEV) e um kernel sem CVE nenhuma.
	bf, err := os.Open("../catalog/bodhi/testdata/updates-F44.json")
	if err != nil {
		t.Fatal(err)
	}
	defer bf.Close()
	page, err := bodhi.ParseUpdates(bf)
	if err != nil {
		t.Fatal(err)
	}
	kernelAntes := bodhi.Update{Alias: "FEDORA-K1", Release: "F44", Severity: "medium",
		Pushed: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), Stable: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
		Builds: []bodhi.Build{{NVR: "kernel-7.2.5-200.fc44", Name: "kernel", Version: "7.2.5", Release: "200.fc44"}}}
	kernelDepois := kernelAntes
	kernelDepois.Alias, kernelDepois.Stable, kernelDepois.Pushed = "FEDORA-K2", time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	kernelDepois.Builds = []bodhi.Build{{NVR: "kernel-7.2.7-200.fc44", Name: "kernel", Version: "7.2.7", Release: "200.fc44"}}
	if err := st.SaveFedoraUpdates(ctx, append(page.Updates, kernelAntes, kernelDepois)); err != nil {
		t.Fatal(err)
	}

	cov, err := st.KEVCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	por := map[string]KEVCoverage{}
	for _, c := range cov {
		por[c.Source] = c
	}
	if m := por["MSRC"]; m.CVEs != 3 || m.InKEV != 2 || m.Vendor != 1 || m.Exploited != 2 {
		t.Errorf("MSRC: %+v", m)
	}
	if a := por["Apple"]; a.InKEV != 1 {
		t.Errorf("Apple: %+v", a)
	}
	if fd := por["Fedora"]; fd.Vendor != -1 || fd.InKEV != 0 {
		t.Errorf("Fedora (o Bodhi não marca exploração): %+v", fd)
	}

	rec, err := st.KEVRecent(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	por2 := map[string]KEVRecentVuln{}
	for _, r := range rec {
		por2[r.CVE] = r
	}
	if len(rec) != 7 || rec[0].CVE != "CVE-2026-88779" {
		t.Fatalf("recentes (desde 01/09, a mais nova primeiro): %d, %v", len(rec), rec[0].CVE)
	}
	if r := por2["CVE-2026-81963"]; !slices.Equal(r.MSRC, []string{"2026-Sep"}) {
		t.Errorf("MSRC da CVE do Windows: %+v", r.MSRC)
	}
	if r := por2["CVE-2026-86950"]; !slices.Equal(r.Apple, []string{"26.7.1"}) {
		t.Errorf("Apple: %+v", r.Apple)
	}
	// Kernel incluído no KEV em 18/09: o primeiro update depois disso é o de 23/09, não o de 13/09.
	if r := por2["CVE-2025-39964"]; len(r.Inferred) != 1 || r.Inferred[0].Alias != "FEDORA-K2" {
		t.Errorf("kernel inferido: %+v", r.Inferred)
	}
	// Chromium incluído em 09/09: o update de 05/10 é o primeiro do recorte (inferido).
	if r := por2["CVE-2026-87491"]; len(r.Inferred) != 1 || r.Inferred[0].NVR != "chromium-154.0.8037.92-1.fc44" {
		t.Errorf("chromium inferido: %+v", r.Inferred)
	}
	// Produto sem pacote no Fedora: nada inferido.
	if r := por2["CVE-2026-88779"]; len(r.Inferred) != 0 {
		t.Errorf("NetScaler não tem pacote no Fedora: %+v", r.Inferred)
	}

	info, err := st.KEVInfo(ctx)
	if err != nil || info.Total != 10 || info.Ransomware != 1 || info.Released != cat.Released {
		t.Errorf("resumo: %+v %v", info, err)
	}
}
