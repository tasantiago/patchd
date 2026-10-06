package bodhi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClient(t *testing.T) {
	releases, err := os.ReadFile("testdata/releases-current.json")
	if err != nil {
		t.Fatal(err)
	}
	updates, err := os.ReadFile("testdata/updates-F44.json")
	if err != nil {
		t.Fatal(err)
	}
	var pedidos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos = append(pedidos, r.URL.RequestURI())
		if r.Header.Get("Accept") != "application/json" {
			// Como o Bodhi: sem o Accept, vem HTML.
			_, _ = w.Write([]byte("<html>Bodhi</html>"))
			return
		}
		switch r.URL.Path {
		case "/releases/":
			_, _ = w.Write(releases)
		case "/updates/":
			q := r.URL.Query()
			if q.Get("type") != "security" || q.Get("status") != "stable" || q.Get("releases") != "F44" {
				http.Error(w, "filtro errado", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(updates)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.BaseURL = srv.URL
	ctx := context.Background()

	rs, err := c.Releases(ctx)
	if err != nil || len(rs) != 2 || rs[0].Name != "F43" {
		t.Fatalf("versões: %+v %v", rs, err)
	}

	p, err := c.Updates(ctx, "F44", time.Time{}, 1)
	if err != nil || len(p.Updates) != 3 {
		t.Fatalf("updates: %+v %v", p, err)
	}
	if strings.Contains(pedidos[len(pedidos)-1], "pushed_since") {
		t.Errorf("sem data, sem pushed_since: %s", pedidos[len(pedidos)-1])
	}

	since := time.Date(2026, 10, 4, 1, 5, 36, 0, time.FixedZone("AMT", -4*3600))
	if _, err := c.Updates(ctx, "F44", since, 2); err != nil {
		t.Fatal(err)
	}
	ultimo := pedidos[len(pedidos)-1]
	if !strings.Contains(ultimo, "pushed_since=2026-10-04T05%3A05%3A36") || !strings.Contains(ultimo, "page=2") {
		t.Errorf("pushed_since em UTC e página: %s", ultimo)
	}

	if _, err := c.Updates(ctx, "F43", time.Time{}, 1); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("erro do Bodhi precisa trazer o status: %v", err)
	}
	for _, ruim := range []string{"EPEL-9", "F44&type=bugfix", ""} {
		if _, err := c.Updates(ctx, ruim, time.Time{}, 1); err == nil || !strings.Contains(err.Error(), "inválida") {
			t.Errorf("versão %q: %v", ruim, err)
		}
	}

	c.MaxPageSize = 10
	if _, err := c.Releases(ctx); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
