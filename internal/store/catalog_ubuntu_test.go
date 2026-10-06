package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/osv"
)

func usnDoTestdata(t *testing.T, id string) osv.Record {
	t.Helper()
	f, err := os.Open("../catalog/osv/testdata/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := osv.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCatalogoUbuntu(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	openssl := usnDoTestdata(t, "USN-8861-1")
	gst := usnDoTestdata(t, "USN-8863-1")
	gst.Fixes = append(gst.Fixes, gst.Fixes[0]) // repetida: fica uma

	// Nanossegundos: o texto precisa voltar idêntico, ou a sincronização baixaria de novo.
	const m1 = "2026-10-02T12:17:59.329313578Z"
	if err := st.SaveUbuntuUSN(ctx, openssl, m1); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUbuntuUSN(ctx, gst, "2026-10-01T16:46:33Z"); err != nil {
		t.Fatal(err)
	}
	v, err := st.UbuntuVersions(ctx)
	if err != nil || v["USN-8861-1"] != m1 || len(v) != 2 {
		t.Fatalf("versões: %v %v", v, err)
	}

	fx, err := st.UbuntuFixes(ctx, "openssl", "26.04")
	if err != nil || len(fx) != 1 || fx[0].Version != "3.5.5-1ubuntu3.7" || fx[0].CVEs != 2 || fx[0].TopPriority != "low" {
		t.Fatalf("openssl no 26.04: %+v %v", fx, err)
	}
	if !fx[0].Published.Equal(time.Date(2026, 10, 1, 12, 2, 0, 0, time.UTC)) {
		t.Errorf("published: %v", fx[0].Published)
	}

	// O filtro de versão não pega o Pro; sem filtro, pega os seis ecossistemas.
	if fx, _ := st.UbuntuFixes(ctx, "gst-plugins-good1.0", "16.04"); len(fx) != 0 {
		t.Errorf("16.04 sem Pro não existe nesse aviso: %+v", fx)
	}
	todos, _ := st.UbuntuFixes(ctx, "gst-plugins-good1.0", "")
	if len(todos) != 6 {
		t.Errorf("todos os ecossistemas: %+v", todos)
	}

	// Gravar de novo substitui.
	openssl.Fixes = nil
	if err := st.SaveUbuntuUSN(ctx, openssl, "2026-10-06T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if fx, _ := st.UbuntuFixes(ctx, "openssl", ""); len(fx) != 0 {
		t.Errorf("a versão nova do aviso não tem correções: %+v", fx)
	}
	// Retirada pelo Ubuntu: a linha fica (com a data nova), as correções e CVEs saem e as
	// consultas a ignoram.
	gst.Withdrawn = time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)
	if err := st.SaveUbuntuUSN(ctx, gst, "2026-10-06T04:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.UbuntuVersions(ctx); v["USN-8863-1"] != "2026-10-06T04:00:00Z" {
		t.Errorf("a retirada precisa ficar guardada, ou volta como nova: %v", v)
	}
	if fx, _ := st.UbuntuFixes(ctx, "gst-plugins-good1.0", ""); len(fx) != 0 {
		t.Errorf("retirada não aparece nas consultas: %+v", fx)
	}
	var orfas int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ubuntu_usn_fixes WHERE usn_id = 'USN-8863-1')
	                                   + (SELECT count(*) FROM ubuntu_usn_cves WHERE usn_id = 'USN-8863-1')`).Scan(&orfas); err != nil || orfas != 0 {
		t.Errorf("correções e CVEs da retirada: %d %v", orfas, err)
	}

	s, err := st.UbuntuSummary(ctx)
	if err != nil || s.USNs != 1 || s.Withdrawn != 1 || s.Fixes != 0 || s.CVEs != 2 {
		t.Errorf("resumo: %+v %v", s, err)
	}
}
