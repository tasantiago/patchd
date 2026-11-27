package thirdparty

import (
	"archive/zip"
	"bytes"
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
		case "/v1/chrome/platforms/win64/channels/stable/versions/all/releases":
			if r.URL.Query().Get("filter") != "endtime=none" {
				http.Error(w, "sem o filtro das servidas", http.StatusBadRequest)
				return
			}
			// Servidas agora: a estável de todos (fixável, 100%), uma de metade do parque, e as
			// da liberação antecipada (0,5%) e a que ainda não saiu (0%), que não valem. A ordem
			// da resposta não é confiável: vale a maior das que contam.
			w.Write([]byte(`{"releases": [{"version": "155.0.8059.26", "fraction": 0.495},
				{"version": "155.0.8059.40", "fraction": 1, "pinnable": true}, {"version": "155.0.8059.39", "fraction": 0.5},
				{"version": "156.0.8100.10", "fraction": 0.005}, {"version": "156.0.8100.11", "fraction": 0}]}`))
		case "/v1/chrome/platforms/mac/channels/stable/versions/all/releases":
			w.Write([]byte(`{"releases": [{"version": "156.0.8100.10", "fraction": 0.005}]}`))
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
		case "/package/notepadplusplus.install":
			w.Write(chocoPkg(t, "notepadplusplus.install", "8.9.8.20261101"))
		case "/repos/microsoft/winget-pkgs/contents/manifests/v/Vazio/Pacote":
			w.Write([]byte(`[{"name": "Beta", "type": "dir"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.ChromeURL, c.FirefoxURL, c.GitHubURL, c.HomebrewURL = srv.URL, srv.URL+"/firefox.json", srv.URL, srv.URL
	c.ChocoURL = srv.URL

	ctx := context.Background()
	for _, tc := range []struct {
		src  Source
		want string
	}{
		{Source{Type: SourceChrome, ID: "win64"}, "155.0.8059.40"},
		{Source{Type: SourceFirefox, ID: "LATEST_FIREFOX_VERSION"}, "157.0.1"},
		{Source{Type: SourceFirefox, ID: "FIREFOX_ESR"}, "140.17.0"},
		{Source{Type: SourceWinget, ID: "7zip.7zip"}, "26.04"},
		{Source{Type: SourceWinget, ID: "Mozilla.Firefox"}, "157.0.1"},
		{Source{Type: SourceWinget, ID: "Notepad++.Notepad++"}, "8.9.8.1"},
		{Source{Type: SourceHomebrew, ID: "visual-studio-code"}, "1.140.0"},
		{Source{Type: SourceChocolatey, ID: "notepadplusplus.install"}, "8.9.8"}, // sem a data da correção do pacote
	} {
		if got, err := c.Latest(ctx, tc.src); err != nil || got != tc.want {
			t.Errorf("%s: %q %v (queria %q)", tc.src, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		src    Source
		trecho string
	}{
		{Source{Type: SourceFirefox, ID: "FIREFOX_NIGHTLY"}, "não está no JSON"},
		{Source{Type: SourceChrome, ID: "mac"}, "nenhuma liberação servida"},
		{Source{Type: SourceWinget, ID: "Vazio.Pacote"}, "nenhuma versão comparável"},
		{Source{Type: SourceWinget, ID: "Nao.Existe"}, "HTTP 404"},
		{Source{Type: SourceWinget, ID: ".x"}, "inválido"},
		{Source{Type: SourceChocolatey, ID: "naoexiste"}, "não existe no Chocolatey"},
	} {
		if _, err := c.Latest(ctx, tc.src); err == nil || !strings.Contains(err.Error(), tc.trecho) {
			t.Errorf("%s: %v (queria %q)", tc.src, err, tc.trecho)
		}
	}
}

// O Evergreen: a resposta é uma lista de linhas (uma por canal, plataforma, arquitetura); o
// filtro escolhe as que contam, sem diferença de maiúsculas no campo e no valor. A API pede
// um User-Agent próprio.
func TestEvergreen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "patchd-teste" {
			http.Error(w, `{"message": "Invalid or missing User-Agent header"}`, http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/app/MicrosoftVisualStudioCode":
			w.Write([]byte(`[
			  {"Version": "1.141.0", "Platform": "win32-x64", "Channel": "Insider", "Architecture": "x64", "URI": "https://x/i"},
			  {"Version": "1.140.2", "Platform": "win32-x64", "Channel": "Stable", "Architecture": "x64", "URI": "https://x/s"},
			  {"Version": "1.140.2", "Platform": "win32-arm64", "Channel": "Stable", "Architecture": "ARM64", "URI": "https://x/a"},
			  {"Version": "1.139.0", "Platform": "win32-x64-user", "Channel": "Stable", "Architecture": "x64", "URI": "https://x/u"}]`))
		case "/app/NotepadPlusPlus":
			// Uma linha só, como objeto (versões antigas da API).
			w.Write([]byte(`{"Version": "8.9.9", "Architecture": "x64", "Type": "exe", "URI": "https://x/n"}`))
		case "/app/SemVersao":
			w.Write([]byte(`[{"Version": "RateLimited", "URI": "https://docs.github.com"}]`))
		default:
			http.Error(w, `{"message": "Application not found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.EvergreenURL = srv.URL
	ctx := context.Background()
	for _, tc := range []struct {
		src  Source
		want string
	}{
		{Source{Type: SourceEvergreen, ID: "MicrosoftVisualStudioCode", Filter: map[string]string{"channel": "STABLE", "Platform": "win32-x64"}}, "1.140.2"},
		{Source{Type: SourceEvergreen, ID: "MicrosoftVisualStudioCode"}, "1.141.0"},
		{Source{Type: SourceEvergreen, ID: "NotepadPlusPlus"}, "8.9.9"},
	} {
		if got, err := c.Latest(ctx, tc.src); err != nil || got != tc.want {
			t.Errorf("%s: %q %v (queria %q)", tc.src, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		src    Source
		trecho string
	}{
		{Source{Type: SourceEvergreen, ID: "MicrosoftVisualStudioCode", Filter: map[string]string{"Channel": "Beta"}}, "nenhuma das 4 linhas"},
		{Source{Type: SourceEvergreen, ID: "SemVersao"}, "nenhuma versão comparável"},
		{Source{Type: SourceEvergreen, ID: "NaoExiste"}, "HTTP 404"},
	} {
		if _, err := c.Latest(ctx, tc.src); err == nil || !strings.Contains(err.Error(), tc.trecho) {
			t.Errorf("%s: %v (queria %q)", tc.src, err, tc.trecho)
		}
	}
	if s := (Source{Type: SourceEvergreen, ID: "X", Filter: map[string]string{"b": "2", "a": "1"}}).String(); s != "evergreen:X[a=1,b=2]" {
		t.Errorf("nome da fonte com filtro: %s", s)
	}
}

// O endoflife.date: os ciclos de um produto, com a data do fim (ou nenhuma) e a mais nova.
func TestLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/products/python":
			w.Write([]byte(`{"schema_version": "1.2.1", "generated_at": "2026-11-15T00:00:00+00:00", "last_modified": "2026-11-01T00:00:00+00:00",
			  "result": {"name": "python", "label": "Python", "releases": [
			    {"name": "3.14", "codename": null, "label": "3.14", "releaseDate": "2025-10-07", "isLts": false, "ltsFrom": null,
			     "isEoas": false, "eoasFrom": "2027-10-01", "isEol": false, "eolFrom": "2030-10-31", "isMaintained": true,
			     "latest": {"name": "3.14.10", "date": "2026-11-04", "link": null}},
			    {"name": "3.9", "codename": null, "label": "3.9", "releaseDate": "2020-10-05", "isLts": false, "ltsFrom": null,
			     "isEol": true, "eolFrom": "2025-10-31", "isMaintained": false, "latest": {"name": "3.9.25", "date": null, "link": null}},
			    {"name": "4.0", "codename": null, "label": "4.0", "releaseDate": "2030-01-01", "isLts": false, "ltsFrom": null,
			     "isEol": false, "eolFrom": null, "isMaintained": true, "latest": null}]}}`))
		case "/api/v1/products/vazio":
			w.Write([]byte(`{"result": {"name": "vazio", "releases": []}}`))
		case "/api/v1/products/data-ruim":
			w.Write([]byte(`{"result": {"name": "data-ruim", "releases": [{"name": "1", "isEol": false, "eolFrom": "31/12/2030"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.EndoflifeURL = srv.URL
	ctx := context.Background()
	cs, err := c.Lifecycle(ctx, "python")
	if err != nil || len(cs) != 3 {
		t.Fatalf("python: %+v %v", cs, err)
	}
	if c := cs[0]; c.Product != "python" || c.Name != "3.14" || c.IsEOL || c.EOLFrom == nil || c.EOLFrom.Format("2006-01-02") != "2030-10-31" || c.Latest != "3.14.10" {
		t.Errorf("3.14: %+v", c)
	}
	if c := cs[1]; !c.IsEOL || c.Latest != "3.9.25" {
		t.Errorf("3.9: %+v", c)
	}
	if c := cs[2]; c.EOLFrom != nil || c.Latest != "" {
		t.Errorf("4.0 sem data nem versão: %+v", c)
	}
	for produto, trecho := range map[string]string{"vazio": "0 ciclos", "data-ruim": "ilegível", "nao-existe": "HTTP 404", "Maiusculo": "inválido", "../x": "inválido"} {
		if _, err := c.Lifecycle(ctx, produto); err == nil || !strings.Contains(err.Error(), trecho) {
			t.Errorf("%s: %v (queria %q)", produto, err, trecho)
		}
	}
}

// chocoPkg: um .nupkg mínimo, com a versão e o script que só baixa.
func chocoPkg(t *testing.T, id, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range map[string]string{
		id + ".nuspec":                `<package><metadata><id>` + id + `</id><version>` + version + `</version></metadata></package>`,
		"tools/chocolateyInstall.ps1": "$url64 = 'https://example.com/x.exe'\n",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

// A busca do Evergreen: a lista de aplicativos (/apps), a procura pelo nome (o igual primeiro)
// e as linhas do aplicativo para a tela, com o filtro.
func TestBuscaNoEvergreen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apps":
			w.Write([]byte(`[{"Name":"GoogleChromeDriver","Application":"Google Chrome Driver","Link":"https://x"},{"Name":"GoogleChrome","Application":"Google Chrome Enterprise","Link":"https://y"},{"Name":"nome com espaço"}]`))
		case "/app/Handbrake":
			w.Write([]byte(`[{"Version":"1.11.2","Architecture":"x64","Type":"exe","InstallerType":"Default","Sha256":"` + strings.Repeat("a", 64) + `","Size":1,"URI":"https://g/x.exe"},
				{"Version":"1.11.2","Architecture":"arm64","Type":"zip","InstallerType":"Default","URI":"https://g/x.zip"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("teste")
	c.EvergreenURL = srv.URL
	apps, err := c.EvergreenApps(context.Background())
	if err != nil || len(apps) != 2 {
		t.Fatalf("lista: %+v %v", apps, err)
	}
	if got := FindEvergreenApps(apps, "googlechrome", 10); len(got) != 2 || got[0].Name != "GoogleChrome" {
		t.Errorf("o igual primeiro: %+v", got)
	}
	if got := FindEvergreenApps(apps, "driver", 10); len(got) != 1 || got[0].Name != "GoogleChromeDriver" {
		t.Errorf("pelo nome do programa: %+v", got)
	}
	if got := FindEvergreenApps(apps, "chrome", 1); len(got) != 1 {
		t.Errorf("o máximo: %+v", got)
	}
	rows, err := c.EvergreenRowsOf(context.Background(), "Handbrake")
	if err != nil || len(rows) != 2 {
		t.Fatalf("linhas: %v %v", rows, err)
	}
	v := EvergreenRowsView(rows, map[string]string{"Type": "exe"}, 10)
	if len(v) != 1 || !v[0].HasSHA || v[0].Architecture != "x64" || len(v[0].Other) != 1 || v[0].Other[0] != "InstallerType=Default" {
		t.Errorf("tela: %+v", v)
	}
	if _, err := c.EvergreenRowsOf(context.Background(), "../x"); err == nil {
		t.Error("nome inválido aceito")
	}
}
