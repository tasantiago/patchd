package thirdparty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "patchd-teste" {
			http.Error(w, "sem User-Agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/chrome/platforms/win64/channels/stable/versions":
			// A ordem da resposta não é confiável: vale a maior.
			w.Write([]byte(`{"versions": [{"version": "155.0.8059.39"}, {"version": "155.0.8059.40"}, {"version": "155.0.8059.26"}]}`))
		case "/firefox.json":
			w.Write([]byte(`{"LATEST_FIREFOX_VERSION": "157.0.1", "FIREFOX_ESR": "140.17.0esr"}`))
		case "/repos/microsoft/winget-pkgs/contents/manifests/7/7zip/7zip":
			w.Write([]byte(`[{"name": "26.02", "type": "dir"}, {"name": "26.04", "type": "dir"}, {"name": "9.20", "type": "dir"}]`))
		case "/repos/microsoft/winget-pkgs/contents/manifests/m/Mozilla/Firefox":
			// Pastas que não são versões: os pacotes vizinhos (ESR, idiomas) e um arquivo.
			w.Write([]byte(`[{"name": "157.0.1", "type": "dir"}, {"name": "157.0", "type": "dir"}, {"name": "ESR", "type": "dir"}, {"name": "pt-BR", "type": "dir"}, {"name": "999.txt", "type": "file"}]`))
		case "/repos/microsoft/winget-pkgs/contents/manifests/n/Notepad++/Notepad++":
			w.Write([]byte(`[{"name": "8.9.8", "type": "dir"}, {"name": "8.9.8.1", "type": "dir"}]`))
		case "/api/cask/visual-studio-code.json":
			w.Write([]byte(`{"token": "visual-studio-code", "version": "1.140.0,abc123"}`))
		case "/repos/microsoft/winget-pkgs/contents/manifests/v/Vazio/Pacote":
			w.Write([]byte(`[{"name": "Beta", "type": "dir"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.ChromeURL, c.FirefoxURL, c.GitHubURL, c.HomebrewURL = srv.URL, srv.URL+"/firefox.json", srv.URL, srv.URL

	ctx := context.Background()
	for src, want := range map[Source]string{
		{SourceChrome, "win64"}:                   "155.0.8059.40",
		{SourceFirefox, "LATEST_FIREFOX_VERSION"}: "157.0.1",
		{SourceFirefox, "FIREFOX_ESR"}:            "140.17.0",
		{SourceWinget, "7zip.7zip"}:               "26.04",
		{SourceWinget, "Mozilla.Firefox"}:         "157.0.1",
		{SourceWinget, "Notepad++.Notepad++"}:     "8.9.8.1",
		{SourceHomebrew, "visual-studio-code"}:    "1.140.0",
	} {
		if got, err := c.Latest(ctx, src); err != nil || got != want {
			t.Errorf("%s: %q %v (queria %q)", src, got, err, want)
		}
	}
	for src, trecho := range map[Source]string{
		{SourceFirefox, "FIREFOX_NIGHTLY"}: "não está no JSON",
		{SourceWinget, "Vazio.Pacote"}:     "nenhuma versão comparável",
		{SourceWinget, "Nao.Existe"}:       "HTTP 404",
		{SourceWinget, ".x"}:               "inválido",
	} {
		if _, err := c.Latest(ctx, src); err == nil || !strings.Contains(err.Error(), trecho) {
			t.Errorf("%s: %v (queria %q)", src, err, trecho)
		}
	}
}
