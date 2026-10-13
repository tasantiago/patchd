package winget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPackagePath(t *testing.T) {
	for id, want := range map[string]string{
		"7zip.7zip":                 "manifests/7/7zip/7zip",
		"Google.Chrome":             "manifests/g/Google/Chrome",
		"Mozilla.Firefox.ESR.pt-BR": "manifests/m/Mozilla/Firefox/ESR/pt-BR",
		"Notepad++.Notepad++":       "manifests/n/Notepad++/Notepad++",
		"Python.Python.3.14":        "manifests/p/Python/Python/3/14",
	} {
		if got, err := PackagePath(id); err != nil || got != want {
			t.Errorf("%s: %q %v", id, got, err)
		}
	}
	for _, id := range []string{"", "Chrome", "Google..Chrome", "../x.y", "a.b/c", "a.b c", ".a.b"} {
		if _, err := PackagePath(id); err == nil {
			t.Errorf("%q devia ser recusado", id)
		}
	}
}

// githubFalso responde como a API do GitHub (lista) e o raw (manifesto).
func githubFalso(t *testing.T) (*Client, *[]string) {
	t.Helper()
	chrome, err := os.ReadFile("testdata/Google.Chrome.installer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var pedidos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos = append(pedidos, r.URL.Path)
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "sem user-agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/repos/microsoft/winget-pkgs/contents/manifests/g/Google/Chrome":
			w.Write([]byte(`[{"name":"Beta","type":"dir"},{"name":"155.0.8059.40","type":"dir"},{"name":"99.0.1","type":"dir"},
				{"name":"155.0.8059.9","type":"dir"},{"name":"README.md","type":"file"},{"name":"EXE","type":"dir"}]`))
		case "/api/repos/microsoft/winget-pkgs/contents/manifests/l/Limite/Pedidos":
			http.Error(w, "rate limit", http.StatusForbidden)
		case "/raw/manifests/g/Google/Chrome/155.0.8059.40/Google.Chrome.installer.yaml":
			w.Write(chrome)
		case "/raw/manifests/g/Google/Chrome/1.0/Google.Chrome.installer.yaml":
			w.Write(chrome) // o arquivo de outra versão: tem de ser recusado
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient("patchd-teste")
	c.APIURL, c.RawURL = srv.URL+"/api", srv.URL+"/raw"
	return c, &pedidos
}

func TestClienteWinget(t *testing.T) {
	c, _ := githubFalso(t)
	ctx := context.Background()
	v, err := c.Versions(ctx, "Google.Chrome")
	if err != nil || strings.Join(v, " ") != "155.0.8059.40 155.0.8059.9 99.0.1" {
		t.Fatalf("versões (numéricas, da mais nova): %v %v", v, err)
	}
	m, err := c.Installer(ctx, "Google.Chrome", v[0])
	if err != nil || len(m.Installers) != 3 || m.Installers[1].Type != "wix" {
		t.Fatalf("manifesto: %+v %v", m, err)
	}
	if _, err := c.Installer(ctx, "Google.Chrome", "1.0"); err == nil || !strings.Contains(err.Error(), "é de Google.Chrome 155.0.8059.40") {
		t.Errorf("manifesto de outra versão: %v", err)
	}
	if _, err := c.Versions(ctx, "Nao.Existe"); err == nil || !strings.Contains(err.Error(), "não existe no winget-pkgs (HTTP 404)") {
		t.Errorf("404: %v", err)
	}
	if _, err := c.Versions(ctx, "Limite.Pedidos"); err == nil || !strings.Contains(err.Error(), "60 pedidos por hora") {
		t.Errorf("403: %v", err)
	}
	if _, err := c.Installer(ctx, "Google.Chrome", "../../x"); err == nil {
		t.Error("versão com caminho devia ser recusada")
	}
}
