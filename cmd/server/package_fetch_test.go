package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ambientePacotes: um fabricante https e um winget-pkgs falso. O 7-Zip confere; o Chrome
// (endereço sem versão) já mudou de build; o "Versionado" mudou num endereço com versão.
func ambientePacotes(t *testing.T) (*winget.Client, *pkgcache.Fetcher, thirdparty.Manifest, []byte) {
	t.Helper()
	zip := []byte(strings.Repeat("7-Zip 26.04 ", 500))
	chromeVelho, chromeNovo := []byte("chrome build .40"), []byte("chrome build .55")
	vend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/7z2604-x64.exe":
			w.Write(zip)
		case "/chrome/googlechromestandaloneenterprise64.msi":
			w.Write(chromeNovo)
		case "/v/9.1/setup.exe":
			w.Write([]byte("trocado"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(vend.Close)
	manifesto := func(id, ver, typ, url, sum string) string {
		return "PackageIdentifier: " + id + "\nPackageVersion: " + ver + "\nInstallerType: " + typ +
			"\nScope: machine\nInstallers:\n- Architecture: x64\n  InstallerUrl: " + url + "\n  InstallerSha256: " + strings.ToUpper(sum) +
			"\n  InstallerSwitches:\n    Silent: /S\nManifestType: installer\nManifestVersion: 1.12.0\n"
	}
	files := map[string]string{
		"/raw/manifests/7/7zip/7zip/26.04/7zip.7zip.installer.yaml": manifesto("7zip.7zip", "26.04", "exe", vend.URL+"/a/7z2604-x64.exe", sha(zip)),
		"/raw/manifests/g/Google/Chrome/155.0.8059.40/Google.Chrome.installer.yaml": manifesto("Google.Chrome", "155.0.8059.40", "wix",
			vend.URL+"/chrome/googlechromestandaloneenterprise64.msi", sha(chromeVelho)),
		"/raw/manifests/e/Empresa/Versionado/9.1/Empresa.Versionado.installer.yaml": manifesto("Empresa.Versionado", "9.1", "exe",
			vend.URL+"/v/9.1/setup.exe", sha([]byte("original"))),
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case files[r.URL.Path] != "":
			w.Write([]byte(files[r.URL.Path]))
		case strings.HasSuffix(r.URL.Path, "/7/7zip/7zip"):
			w.Write([]byte(`[{"name":"26.04","type":"dir"}]`))
		case strings.HasSuffix(r.URL.Path, "/g/Google/Chrome"):
			w.Write([]byte(`[{"name":"155.0.8059.40","type":"dir"}]`))
		case strings.HasSuffix(r.URL.Path, "/e/Empresa/Versionado"):
			w.Write([]byte(`[{"name":"9.1","type":"dir"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	c := winget.NewClient("patchd-teste")
	c.APIURL, c.RawURL = gh.URL+"/api", gh.URL+"/raw"
	f := pkgcache.NewFetcher("patchd-teste")
	f.HTTP = vend.Client()
	f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	f.Wait = time.Millisecond
	m, err := thirdparty.Parse([]byte(`{"version": 1, "apps": [
		{"id": "google-chrome", "os": "windows", "name": "Google Chrome", "category": "minimum", "match": "^Google Chrome$", "min_version": "1",
		 "install": {"winget": "Google.Chrome"}},
		{"id": "7zip", "os": "windows", "name": "7-Zip", "category": "minimum", "match": "^7-Zip", "min_version": "1",
		 "install": {"winget": "7zip.7zip", "type": "exe"}},
		{"id": "versionado", "os": "windows", "name": "Versionado", "category": "minimum", "match": "^V", "min_version": "1",
		 "install": {"winget": "Empresa.Versionado"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c, f, m, zip
}

func TestPackageFetchEList(t *testing.T) {
	c, f, m, zip := ambientePacotes(t)
	st := store.NewMemory()
	dir := filepath.Join(t.TempDir(), "pacotes")
	ctx := context.Background()
	fetch := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packageFetch(ctx, c, f, st, m, dir, args, &out, &errb)
		return code, out.String() + errb.String()
	}
	list := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packageList(ctx, st, dir, args, time.Now(), &out, &errb)
		return code, out.String() + errb.String()
	}
	zipFile := sha(zip) + ".exe"

	code, out := fetch("-all")
	for _, tr := range []string{
		"7-Zip (7zip) ← winget 7zip.7zip 26.04 · exe · x64\n",
		"  baixado     pacotes/" + zipFile + " (5,9 KiB) em ",
		"  registrado  novo\n",
		"Google Chrome (google-chrome) ← winget Google.Chrome 155.0.8059.40 · wix · x64\n",
		"  RECUSADO    o SHA-256 do arquivo baixado (",
		"o endereço não tem a versão (155.0.8059.40): o fabricante já publicou outra build",
		"o endereço tem a versão 9.1: um arquivo diferente num endereço versionado é\n              suspeito",
	} {
		if !strings.Contains(out, tr) {
			t.Errorf("fetch -all: faltou %q em\n%s", tr, out)
		}
	}
	if code != exitRuntime {
		t.Errorf("fetch -all com recusas: código %d", code)
	}
	// Só o 7-Zip ficou: na pasta e no registro.
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || ents[0].Name() != zipFile {
		t.Errorf("pasta: %v", ents)
	}
	l, _ := st.ThirdPartyPackages(ctx)
	if len(l) != 1 || l[0].AppID != "7zip" || l[0].Arguments != "/S" || l[0].Size != int64(len(zip)) {
		t.Fatalf("registro: %+v", l)
	}

	// De novo: do cache, conferido; o registro já estava.
	if code, out := fetch("-app", "7zip"); code != exitOK || !strings.Contains(out, "  no cache    pacotes/"+zipFile+" (5,9 KiB), conferido\n") ||
		!strings.Contains(out, "  registrado  já estava\n") {
		t.Errorf("fetch de novo: %d\n%s", code, out)
	}

	if code, out := list(); code != exitOK || !strings.Contains(out, "7zip      26.04   exe   x64  5,9 KiB  "+sha(zip)[:12]+"  agora       presente") {
		t.Errorf("list: %d\n%s", code, out)
	}
	if code, out := list("-verify"); code != exitOK || !strings.Contains(out, "conferido") {
		t.Errorf("list -verify: %d\n%s", code, out)
	}
	// Arquivo adulterado com o mesmo tamanho: só o -verify percebe.
	adulterado := bytes.Replace(zip, []byte("7-Zip"), []byte("7-Zap"), 1)
	os.WriteFile(filepath.Join(dir, zipFile), adulterado, 0o640)
	if code, out := list(); code != exitOK || !strings.Contains(out, "presente") {
		t.Errorf("list sem -verify: %d\n%s", code, out)
	}
	if code, out := list("-verify"); code != exitRuntime || !strings.Contains(out, "DIFERENTE: conteúdo com SHA-256 "+sha(adulterado)[:12]) {
		t.Errorf("list -verify adulterado: %d\n%s", code, out)
	}
	os.Remove(filepath.Join(dir, zipFile))
	if code, out := list(); code != exitRuntime || !strings.Contains(out, "SUMIU") {
		t.Errorf("list sem o arquivo: %d\n%s", code, out)
	}
}

// A mesma versão já registrada com outro binário: o download confere com o manifesto,
// mas o registro recusa.
func TestPackageFetchMesmaVersaoOutroBinario(t *testing.T) {
	c, f, m, zip := ambientePacotes(t)
	st := store.NewMemory()
	st.SaveThirdPartyPackage(context.Background(), store.ThirdPartyPackage{AppID: "7zip", WingetID: "7zip.7zip", Version: "26.04",
		Architecture: "x64", Type: "exe", URL: "https://x", SHA256: strings.Repeat("ab", 32), Size: 10})
	var out bytes.Buffer
	code := packageFetch(context.Background(), c, f, st, m, t.TempDir(), []string{"-app", "7zip"}, &out, &out)
	if code != exitRuntime || !strings.Contains(out.String(), "  RECUSADO    7zip 26.04 (exe, x64) já está guardado com o SHA-256 abababababab…; o manifesto agora diz "+sha(zip)[:12]) {
		t.Errorf("%d\n%s", code, out.String())
	}
}

func TestPackageFetchOpcoes(t *testing.T) {
	c, f, m, _ := ambientePacotes(t)
	for _, cs := range []struct {
		args []string
		msg  string
	}{
		{nil, "use um (e só um) de -app e -all"},
		{[]string{"-file", "x.yaml"}, "flag provided but not defined: -file"},
		{[]string{"-app", "nao-existe"}, `a regra "nao-existe" do Windows não existe`},
	} {
		var out, errb bytes.Buffer
		if code := packageFetch(context.Background(), c, f, store.NewMemory(), m, t.TempDir(), cs.args, &out, &errb); code != exitConfig ||
			!strings.Contains(errb.String(), cs.msg) {
			t.Errorf("%v: %d %s", cs.args, code, errb.String())
		}
	}
	var out, errb bytes.Buffer
	look := func(k string) (string, bool) {
		return map[string]string{"PATCHD_DATABASE_URL": "postgres://x"}[k], k == "PATCHD_DATABASE_URL"
	}
	if code := run([]string{"package", "fetch", "-app", "7zip"}, look, &out, &errb); code != exitConfig || !strings.Contains(errb.String(), "defina PATCHD_CONTENT_DIR") {
		t.Errorf("sem pasta: %d %s", code, errb.String())
	}
}
