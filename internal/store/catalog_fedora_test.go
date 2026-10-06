package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
)

func TestCatalogoFedora(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	f, err := os.Open("../catalog/bodhi/testdata/updates-F44.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	page, err := bodhi.ParseUpdates(f)
	if err != nil {
		t.Fatal(err)
	}

	if last, err := st.FedoraLastPushed(ctx, "F44"); err != nil || !last.IsZero() {
		t.Fatalf("sem updates, sem data: %v %v", last, err)
	}
	// O chromium repetido (veio em duas páginas) fica uma vez.
	lote := append(page.Updates, page.Updates[0])
	if err := st.SaveFedoraUpdates(ctx, lote); err != nil {
		t.Fatal(err)
	}
	last, err := st.FedoraLastPushed(ctx, "F44")
	if err != nil || !last.Equal(time.Date(2026, 10, 5, 1, 5, 36, 0, time.UTC)) {
		t.Fatalf("maior date_pushed: %v %v", last, err)
	}

	fx, err := st.FedoraFixes(ctx, "chromium", "F44")
	if err != nil || len(fx) != 1 || fx[0].CVEs != 27 || !fx[0].CVEsPartial || fx[0].Severity != "urgent" {
		t.Fatalf("chromium: %+v %v", fx, err)
	}
	if fx, _ := st.FedoraFixes(ctx, "openssl", ""); len(fx) != 1 || fx[0].Epoch != 1 || fx[0].NVR != "openssl-3.5.4-2.fc44" {
		t.Errorf("openssl com época: %+v", fx)
	}
	if fx, _ := st.FedoraFixes(ctx, "chromium", "F43"); len(fx) != 0 {
		t.Errorf("filtro por versão: %+v", fx)
	}

	// Regravar substitui: o update do openssl perde um build e as CVEs.
	o := page.Updates[1]
	o.Builds, o.CVEs = o.Builds[:1], nil
	if err := st.SaveFedoraUpdates(ctx, []bodhi.Update{o}); err != nil {
		t.Fatal(err)
	}
	if fx, _ := st.FedoraFixes(ctx, "python-cryptography", ""); len(fx) != 0 {
		t.Errorf("o build removido continua: %+v", fx)
	}

	s, err := st.FedoraSummary(ctx)
	if err != nil || len(s) != 1 || s[0].Release != "F44" || s[0].Updates != 3 || s[0].Partial != 1 || s[0].CVEs != 27 {
		t.Errorf("resumo: %+v %v", s, err)
	}
	if err := st.SaveFedoraUpdates(ctx, nil); err != nil {
		t.Errorf("lote vazio: %v", err)
	}
}
