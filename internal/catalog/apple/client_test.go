package apple

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	gdmf, err := os.ReadFile("testdata/gdmf-pmv-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	sofa, err := os.ReadFile("testdata/sofa-macos-v2-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/pmv":
			_, _ = w.Write(gdmf)
		case "/v2/macos_data_feed.json":
			_, _ = w.Write(sofa)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient("patchd-teste")
	if err != nil {
		t.Fatal(err)
	}
	c.GDMFURL, c.SOFAURL = srv.URL+"/v2/pmv", srv.URL+"/v2/macos_data_feed.json"
	ctx := context.Background()

	// O cliente do gdmf só confia na raiz da Apple: o certificado do servidor de teste
	// (como o de qualquer um que se passe pelo gdmf) é recusado.
	if _, _, err := c.FetchGDMF(ctx); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("certificado fora da raiz da Apple deveria ser recusado: %v", err)
	}
	// E o proxy do ambiente continua valendo no transporte com a raiz fixada.
	if tr, ok := c.GDMF.Transport.(*http.Transport); !ok || tr.Proxy == nil {
		t.Error("o transporte do gdmf perdeu o proxy do ambiente")
	}

	// Com o cliente do servidor de teste, os dois feeds são lidos.
	c.GDMF, c.SOFA = srv.Client(), srv.Client()
	vs, hash, err := c.FetchGDMF(ctx)
	if err != nil || len(vs) != 10 || len(hash) != 64 {
		t.Fatalf("gdmf: %d versões, hash %q, %v", len(vs), hash, err)
	}
	f, err := c.FetchSOFA(ctx)
	if err != nil || len(f.Releases) != 7 {
		t.Fatalf("sofa: %+v %v", f, err)
	}

	c.SOFAURL = srv.URL + "/nada"
	if _, err := c.FetchSOFA(ctx); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	c.SOFAURL = "http://exemplo.invalid/feed.json"
	if _, err := c.FetchSOFA(ctx); err == nil || !strings.Contains(err.Error(), "exigem https") {
		t.Errorf("http fora de localhost: %v", err)
	}
	c.MaxSize = 10
	if _, _, err := c.FetchGDMF(ctx); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite: %v", err)
	}
}
