package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/protocol"
)

func TestComplianceLinuxConsultas(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	// Máquinas: prefixo único, ambíguo e inexistente.
	for _, id := range []string{"7aa604f7-0001", "7aa6ffff-0002", "287d1717-0003"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, CollectedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if id, ok, err := st.ResolveMachine(ctx, "7aa604"); err != nil || !ok || id != "7aa604f7-0001" {
		t.Errorf("prefixo único: %q %t %v", id, ok, err)
	}
	if _, _, err := st.ResolveMachine(ctx, "7aa6"); !errors.Is(err, ErrMachineAmbiguous) {
		t.Errorf("prefixo ambíguo: %v", err)
	}
	if _, ok, err := st.ResolveMachine(ctx, "ffff"); ok || err != nil {
		t.Errorf("inexistente: %t %v", ok, err)
	}

	// Ubuntu: a USN do openssl (26.04) e a do gst (com Pro); a CVE do openssl no KEV.
	if err := st.SaveUbuntuUSN(ctx, usnDoTestdata(t, "USN-8861-1"), "m1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUbuntuUSN(ctx, usnDoTestdata(t, "USN-8863-1"), "m2"); err != nil {
		t.Fatal(err)
	}
	cat := kev.Catalog{Version: "x", Released: "y", Vulns: []kev.Vuln{
		{CVE: "CVE-2026-42772", Vendor: "OpenSSL", Product: "OpenSSL", Added: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), CWEs: []string{}},
	}}
	if err := st.SaveKEV(ctx, cat); err != nil {
		t.Fatal(err)
	}
	eco, err := st.UbuntuEcosystemsKnown(ctx)
	if err != nil || !slices.Contains(eco, "Ubuntu:26.04:LTS") || !slices.Contains(eco, "Ubuntu:Pro:16.04:LTS") {
		t.Errorf("ecossistemas: %v %v", eco, err)
	}
	av, err := st.UbuntuAdvisories(ctx, "Ubuntu:26.04:LTS", []string{"openssl", "gst-plugins-good1.0", "nada"})
	if err != nil || len(av) != 2 {
		t.Fatalf("avisos do 26.04: %+v %v", av, err)
	}
	for _, a := range av {
		if a.Source == "openssl" && (a.Fixed != "3.5.5-1ubuntu3.7" || len(a.CVEs) != 2 || !slices.Equal(a.KEV, []string{"CVE-2026-42772"}) || a.Severity != "low") {
			t.Errorf("openssl: %+v", a)
		}
	}

	// Fedora: o recorte do F44, com época e sem data.
	f, err := os.Open("../catalog/bodhi/testdata/updates-F44.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	page, err := bodhi.ParseUpdates(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveFedoraUpdates(ctx, page.Updates); err != nil {
		t.Fatal(err)
	}
	if rel, _ := st.FedoraReleasesKnown(ctx); !slices.Equal(rel, []string{"F44"}) {
		t.Errorf("versões do Fedora: %v", rel)
	}
	fa, err := st.FedoraAdvisories(ctx, "F44", []string{"openssl", "chromium"})
	if err != nil || len(fa) != 2 {
		t.Fatalf("avisos do F44: %+v %v", fa, err)
	}
	for _, a := range fa {
		if a.Source == "openssl" && a.Fixed != "1:3.5.4-2.fc44" {
			t.Errorf("EVR com época: %+v", a)
		}
		if a.Source == "chromium" && (!a.Partial || len(a.CVEs) != 27 || a.Published.IsZero()) {
			t.Errorf("chromium: %+v", a)
		}
	}

	ents, err := st.KEVEntries(ctx)
	if err != nil || len(ents) != 1 || ents[0].Vendor != "OpenSSL" {
		t.Errorf("KEV: %+v %v", ents, err)
	}
}
