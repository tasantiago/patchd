package tpfetch

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

	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// O Firefox pelo site da Mozilla: o link com {versao}, o SHA256SUMS com todos os idiomas, e a
// assinatura conferida antes de guardar. Sem o arquivo de somas, vale a assinatura.
func TestFonteDoFabricante(t *testing.T) {
	inst := authsigtest.Exe("Mozilla Corporation")
	outro := authsigtest.Exe("Outra Empresa")
	sums := "0000000000000000000000000000000000000000000000000000000000000000  win64/en-US/Firefox Setup 146.0.1.exe\n" +
		shaHex(inst) + "  win64/pt-BR/Firefox Setup 146.0.1.exe\n" +
		"1111111111111111111111111111111111111111111111111111111111111111  win32/pt-BR/Firefox Setup 146.0.1.exe\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pub/firefox/releases/146.0.1/SHA256SUMS":
			w.Write([]byte(sums))
		case "/pub/firefox/releases/146.0.1/win64/pt-BR/Firefox Setup 146.0.1.exe":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		case "/outro/146.0.1/setup.exe":
			w.Write(outro)
		case "/semassinatura/146.0.1/setup.exe":
			w.Write(authsigtest.Exe(""))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := pkgcache.NewFetcher("patchd-teste")
	f.HTTP = srv.Client()
	f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	f.Wait = time.Millisecond
	st := store.NewMemory()
	ctx := context.Background()
	rule := thirdparty.App{ID: "mozilla-firefox", OS: "windows", Name: "Mozilla Firefox",
		Install: &thirdparty.Install{Winget: "Mozilla.Firefox.pt-BR", Signer: "Mozilla Corporation"}}
	src := store.InstallerSource{AppID: rule.ID, Kind: store.SourceURL, Type: "nullsoft",
		URLTemplate: srv.URL + "/pub/firefox/releases/{versao}/win64/pt-BR/Firefox%20Setup%20{versao}.exe",
		ChecksumURL: srv.URL + "/pub/firefox/releases/{versao}/SHA256SUMS"}
	if err := CheckVendor(src, rule); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var out strings.Builder
	if !Vendor(ctx, srv.Client(), f, st, rule, src, "146.0.1", dir, &out) {
		t.Fatalf("baixar:\n%s", out.String())
	}
	pk, _ := st.ThirdPartyPackages(ctx)
	if len(pk) != 1 || pk[0].SHA256 != shaHex(inst) || pk[0].Version != "146.0.1" || pk[0].Type != "nullsoft" || pk[0].Arguments != "/S" ||
		!strings.HasSuffix(pk[0].URL, "Firefox%20Setup%20146.0.1.exe") {
		t.Fatalf("pacote: %+v", pk)
	}
	if !strings.Contains(out.String(), "(do arquivo do fabricante)") || !strings.Contains(out.String(), "assinatura  Mozilla Corporation") {
		t.Errorf("andamento:\n%s", out.String())
	}
	// Sem o arquivo de somas: o SHA-256 calculado, e a assinatura errada não fica.
	semSoma := store.InstallerSource{AppID: rule.ID, Kind: store.SourceURL, Type: "nullsoft", URLTemplate: srv.URL + "/outro/{versao}/setup.exe"}
	out.Reset()
	if Vendor(ctx, srv.Client(), f, st, rule, semSoma, "146.0.1", dir, &out) || !strings.Contains(out.String(), `assinado por "Outra Empresa"`) {
		t.Errorf("assinatura errada:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, pkgcache.FileName(shaHex(outro), false))); !os.IsNotExist(err) {
		t.Error("o arquivo de outra assinatura ficou")
	}
	semSoma.URLTemplate = srv.URL + "/semassinatura/{versao}/setup.exe"
	out.Reset()
	if Vendor(ctx, srv.Client(), f, st, rule, semSoma, "146.0.1", dir, &out) || !strings.Contains(out.String(), "não é assinado") {
		t.Errorf("sem assinatura:\n%s", out.String())
	}
	// Versão que não é versão, e conferências da fonte.
	out.Reset()
	if Vendor(ctx, srv.Client(), f, st, rule, src, "", dir, &out) || !strings.Contains(out.String(), "versão") {
		t.Errorf("sem versão:\n%s", out.String())
	}
	semAssinante := rule
	semAssinante.Install = &thirdparty.Install{Winget: "x.y"}
	for nome, c := range map[string]struct {
		s    store.InstallerSource
		r    thirdparty.App
		quer string
	}{
		"sem {versao}":    {store.InstallerSource{URLTemplate: "https://x/latest.exe", Type: "nullsoft", ChecksumURL: "https://x/s"}, rule, "{versao}"},
		"http":            {store.InstallerSource{URLTemplate: "http://x/{versao}.exe", Type: "nullsoft"}, rule, "https"},
		"tipo":            {store.InstallerSource{URLTemplate: "https://x/{versao}.zip", Type: "zip"}, rule, "tipo"},
		"exe sem args":    {store.InstallerSource{URLTemplate: "https://x/{versao}.exe", Type: "exe"}, rule, "argumentos"},
		"sem hash nem O=": {store.InstallerSource{URLTemplate: "https://x/{versao}.exe", Type: "nullsoft"}, semAssinante, "assinatura"},
	} {
		if err := CheckVendor(c.s, c.r); err == nil || !strings.Contains(err.Error(), c.quer) {
			t.Errorf("%s: %v", nome, err)
		}
	}
}

func TestSomaDoArquivo(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	sums := []byte(a + "  win64/en-US/Firefox Setup 1.0.msi\n" + b + " *win64/pt-BR/Firefox Setup 1.0.msi\n")
	if got, err := sumFor(sums, "https://x/pub/firefox/releases/1.0/win64/pt-BR/Firefox%20Setup%201.0.msi"); err != nil || got != b {
		t.Errorf("pt-BR: %s %v", got, err)
	}
	if _, err := sumFor(sums, "https://x/setup-2.0.msi"); err == nil {
		t.Error("arquivo que não está nas somas")
	}
	// Uma linha só, sem caminho: vale pelo nome.
	if got, err := sumFor([]byte(b+"  setup.exe\r\n"), "https://x/1.0/setup.exe"); err != nil || got != b {
		t.Errorf("uma linha: %s %v", got, err)
	}
}
