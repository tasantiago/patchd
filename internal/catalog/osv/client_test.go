package osv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	usn, err := os.ReadFile("testdata/USN-8861-1.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Ubuntu/modified_id.csv":
			_, _ = w.Write([]byte("2026-10-05T07:30:02.615241916Z,UBUNTU-CVE-2026-42772\n2026-10-02T12:17:59.329313578Z,USN-8861-1\n"))
		case "/Ubuntu/USN-8861-1.json", "/Ubuntu/USN-8000-1.json": // o segundo devolve o registro errado
			_, _ = w.Write(usn)
		default:
			// Como o bucket: um XML de erro, com status 404.
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<Error><Code>NoSuchKey</Code></Error>"))
		}
	}))
	defer srv.Close()
	c := NewClient("Ubuntu", "patchd-teste")
	c.BaseURL = srv.URL
	ctx := context.Background()

	es, err := c.ModifiedIDs(ctx, "USN-")
	if err != nil || len(es) != 1 || es[0].ID != "USN-8861-1" {
		t.Fatalf("lista: %+v %v", es, err)
	}
	r, err := c.Record(ctx, "USN-8861-1")
	if err != nil || len(r.Fixes) != 1 {
		t.Fatalf("registro: %+v %v", r, err)
	}
	if _, err := c.Record(ctx, "USN-8000-1"); err == nil || !strings.Contains(err.Error(), "recebido o USN-8861-1") {
		t.Errorf("registro trocado: %v", err)
	}
	if _, err := c.Record(ctx, "CVE-2026-42772"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("registro inexistente precisa dar o status, não erro de JSON: %v", err)
	}
	for _, ruim := range []string{"../modified_id", "a/b", ""} {
		if _, err := c.Record(ctx, ruim); err == nil || !strings.Contains(err.Error(), "inválido") {
			t.Errorf("ID %q: %v", ruim, err)
		}
	}

	c.MaxListSize = 10
	if _, err := c.ModifiedIDs(ctx, ""); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
