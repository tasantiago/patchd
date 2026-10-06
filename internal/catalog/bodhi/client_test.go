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

// Como no laboratório: a conexão cai no meio da resposta, ou o Bodhi devolve 503. O
// cliente repete; um 400 não se repete.
func TestClientRepete(t *testing.T) {
	releases, err := os.ReadFile("testdata/releases-current.json")
	if err != nil {
		t.Fatal(err)
	}
	var (
		pedidos int
		falhas  int    // quantos pedidos falham antes do sucesso
		modo    string // "reset", "503" ou "400"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos++
		if pedidos <= falhas {
			switch modo {
			case "reset":
				// Cabeçalhos e metade do corpo, depois a conexão cai.
				w.Header().Set("Content-Length", "100000")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(releases[:50])
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			case "503":
				http.Error(w, "indisponível", http.StatusServiceUnavailable)
				return
			case "400":
				http.Error(w, "pedido ruim", http.StatusBadRequest)
				return
			}
		}
		_, _ = w.Write(releases)
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.BaseURL = srv.URL
	c.RetryWait = time.Millisecond
	ctx := context.Background()

	for _, caso := range []struct {
		modo         string
		falhas       int
		querErro     bool
		querPedidos  int
		trechoDoErro string
	}{
		{"reset", 2, false, 3, ""},
		{"503", 3, false, 4, ""},
		{"503", 4, true, 4, "(4 tentativas)"},
		{"400", 1, true, 1, "400"},
	} {
		pedidos, falhas, modo = 0, caso.falhas, caso.modo
		_, err := c.Releases(ctx)
		if (err != nil) != caso.querErro || pedidos != caso.querPedidos || (err != nil && !strings.Contains(err.Error(), caso.trechoDoErro)) {
			t.Errorf("%s com %d falhas: erro=%v, %d pedidos (esperados %d)", caso.modo, caso.falhas, err, pedidos, caso.querPedidos)
		}
	}

	// Contexto cancelado durante a espera: para na hora.
	pedidos, falhas, modo = 0, 10, "503"
	c.RetryWait = time.Hour
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	inicio := time.Now()
	if _, err := c.Releases(ctx2); err == nil || time.Since(inicio) > 5*time.Second || !strings.Contains(err.Error(), "interrompido") {
		t.Errorf("cancelamento: %v em %s", err, time.Since(inicio))
	}
}
