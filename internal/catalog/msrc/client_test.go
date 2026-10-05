package msrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	recorte, err := os.ReadFile("testdata/2026-Sep-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "sem Accept", http.StatusNotAcceptable)
			return
		}
		switch r.URL.Path {
		case "/updates":
			_, _ = w.Write([]byte(`{"value":[{"ID":"2026-Sep","InitialReleaseDate":"2026-09-08T07:00:00Z","CurrentReleaseDate":"2026-10-04T01:53:47Z"}]}`))
		case "/cvrf/2026-Sep":
			_, _ = w.Write(recorte)
		case "/cvrf/2026-Aug":
			_, _ = w.Write(recorte) // o servidor mandou o documento errado
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.BaseURL = srv.URL
	ctx := context.Background()

	us, err := c.Updates(ctx)
	if err != nil || len(us) != 1 || us[0].ID != "2026-Sep" {
		t.Fatalf("lista: %+v %v", us, err)
	}
	d, err := c.Document(ctx, "2026-Sep")
	if err != nil || d.ID != "2026-Sep" || len(d.Fixes) == 0 {
		t.Fatalf("documento: %v", err)
	}
	if _, err := c.Document(ctx, "2026-Aug"); err == nil || !strings.Contains(err.Error(), "recebido o 2026-Sep") {
		t.Errorf("documento trocado: %v", err)
	}
	if _, err := c.Document(ctx, "2026-Jan"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("inexistente: %v", err)
	}
	if _, err := c.Document(ctx, "../updates"); err == nil || !strings.Contains(err.Error(), "inválido") {
		t.Errorf("ID fora do padrão: %v", err)
	}
	c.MaxDocSize = 1 << 10
	if _, err := c.Document(ctx, "2026-Sep"); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
