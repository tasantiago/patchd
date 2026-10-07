package store

import (
	"context"
	"testing"
	"time"
)

func TestCatalogoExecucoes(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	// A trava: o segundo pedido não espera, volta ok=false; depois do unlock, volta a valer.
	unlock, ok, err := st.TryCatalogLock(ctx)
	if err != nil || !ok {
		t.Fatalf("primeira trava: %t %v", ok, err)
	}
	if _, ok2, err := st.TryCatalogLock(ctx); err != nil || ok2 {
		t.Errorf("segunda trava com a primeira presa: %t %v", ok2, err)
	}
	unlock()
	unlock2, ok, err := st.TryCatalogLock(ctx)
	if err != nil || !ok {
		t.Fatalf("trava depois do unlock: %t %v", ok, err)
	}
	unlock2()

	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		source  string
		minutes int
		ok      bool
		detail  string
	}{
		{"msrc", 0, true, "13 documento(s) gravado(s), 0 com falha"},
		{"msrc", 60, false, "api.msrc.microsoft.com: HTTP 503"},
		{"kev", 10, true, "atualizado"},
		{"kev", 70, true, "em dia"},
	} {
		at := base.Add(time.Duration(r.minutes) * time.Minute)
		if err := st.RecordCatalogRun(ctx, r.source, "agendada", at.Add(-time.Minute), at, r.ok, r.detail); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := st.CatalogStatus(ctx, []string{"msrc", "kev", "fedora"})
	if err != nil || len(ss) != 3 {
		t.Fatalf("situação: %+v %v", ss, err)
	}
	if m := ss[0]; m.Source != "msrc" || !m.LastOK.Equal(base) || !m.LastFail.Equal(base.Add(time.Hour)) || m.Latest || m.Detail != "api.msrc.microsoft.com: HTTP 503" || m.Runs != 2 {
		t.Errorf("msrc: %+v", m)
	}
	if k := ss[1]; !k.Latest || k.Detail != "em dia" || !k.LastFail.IsZero() || k.Trigger != "agendada" {
		t.Errorf("kev: %+v", k)
	}
	if f := ss[2]; f.Source != "fedora" || !f.LastOK.IsZero() || f.Runs != 0 || f.Latest {
		t.Errorf("fedora (nunca rodou): %+v", f)
	}

	if n, err := st.PruneCatalogRuns(ctx, base.Add(30*time.Minute)); err != nil || n != 2 {
		t.Errorf("limpeza: %d %v", n, err)
	}
}
