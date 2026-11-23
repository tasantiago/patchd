package tpfetch

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func pacoteChoco(t *testing.T, id, version, script string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create(id + ".nuspec")
	w.Write([]byte(`<package><metadata><id>` + id + `</id><version>` + version + `</version></metadata></package>`))
	w, _ = zw.Create("tools/chocolateyInstall.ps1")
	w.Write([]byte(script))
	zw.Close()
	return b.Bytes()
}

// O Chrome pelo pacote do Chocolatey: o link e o SHA-256 do script, a versão do pacote, o
// tipo e os argumentos da fonte; o SHA-256 que não confere (o Google já publicou outra
// versão no mesmo link) é recusado.
func TestFonteChocolatey(t *testing.T) {
	inst := authsigtest.Exe("Google LLC")
	var srvURL string
	sha := shaHex(inst)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/package/googlechrome":
			w.Write(pacoteChoco(t, "googlechrome", "159.0.8154.13", "$packageArgs = @{\n  url64bit = '"+srvURL+"/chrome64.exe'\n  checksum64 = '"+sha+"'\n  checksumType64 = 'sha256'\n}\n"))
		case "/api/v2/package/adobereader":
			w.Write(pacoteChoco(t, "adobereader", "2026.3.22110", "$MUIurl64 = '"+srvURL+"/AcroRdrDCx642600322110_MUI.exe'\n$MUIchecksum64 = '"+sha+"'\n"+
				"$DownloadArgs = @{\n  url64bit = $MUIurl64\n  checksum64 = $MUIchecksum64\n}\n"))
		case "/AcroRdrDCx642600322110_MUI.exe":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		case "/api/v2/package/velho":
			w.Write(pacoteChoco(t, "velho", "1.0.0.20250101", "$url64 = '"+srvURL+"/chrome64.exe'\n$checksum64 = '"+strings.Repeat("a", 64)+"'\n"))
		case "/chrome64.exe", "/chrome64.msi":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	f := pkgcache.NewFetcher("patchd-teste")
	f.HTTP = srv.Client()
	f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	f.Wait = time.Millisecond
	st := store.NewMemory()
	ctx := context.Background()
	rule := thirdparty.App{ID: "google-chrome", OS: "windows", Name: "Google Chrome", Install: &thirdparty.Install{Winget: "Google.Chrome", Signer: "Google LLC"}}
	src := store.InstallerSource{AppID: rule.ID, Kind: store.SourceChocolatey, PackageID: "googlechrome", Type: "nullsoft"}
	var out strings.Builder
	if !Choco(ctx, srv.Client(), srv.URL+"/api/v2", f, st, rule, src, "", "", t.TempDir(), &out) {
		t.Fatalf("baixar:\n%s", out.String())
	}
	pk, _ := st.ThirdPartyPackages(ctx)
	if len(pk) != 1 || pk[0].Version != "159.0.8154.13" || pk[0].SHA256 != sha || pk[0].Type != "nullsoft" || pk[0].Arguments != "/S" || pk[0].WingetID != "chocolatey:googlechrome" {
		t.Fatalf("pacote: %+v\n%s", pk, out.String())
	}
	if !strings.Contains(out.String(), "(de url64bit)") || !strings.Contains(out.String(), "(do pacote)") {
		t.Errorf("andamento:\n%s", out.String())
	}
	// O SHA-256 do pacote não confere com o arquivo: recusado.
	out.Reset()
	src.PackageID = "velho"
	if Choco(ctx, srv.Client(), srv.URL+"/api/v2", f, st, rule, src, "", "", t.TempDir(), &out) || !strings.Contains(out.String(), "RECUSADO") ||
		!strings.Contains(out.String(), "versão 1.0.0") {
		t.Errorf("SHA-256 velho:\n%s", out.String())
	}
	// O Adobe: o pacote escreve 2026.3.22110; o Windows, 26.003.22110 (o número está no link).
	out.Reset()
	src.PackageID, src.Type, src.Arguments = "adobereader", "exe", "/sAll /quiet"
	if !Choco(ctx, srv.Client(), srv.URL+"/api/v2", f, st, rule, src, "", "26.003.22110", t.TempDir(), &out) ||
		!strings.Contains(out.String(), "versão 26.003.22110 (a de referência; o pacote escreve 2026.3.22110)") {
		t.Errorf("adobe:\n%s", out.String())
	}
	if pk, _ := st.ThirdPartyPackages(ctx); !slices.ContainsFunc(pk, func(p store.ThirdPartyPackage) bool {
		return p.Version == "26.003.22110" && p.WingetID == "chocolatey:adobereader"
	}) {
		t.Errorf("pacote do adobe: %+v", pk)
	}
	// O link .exe com o tipo msi: recusado antes de baixar.
	out.Reset()
	exe := src
	exe.Type = "msi"
	exe.Arguments = ""
	if Choco(ctx, srv.Client(), srv.URL+"/api/v2", f, st, rule, exe, "", "", t.TempDir(), &out) || !strings.Contains(out.String(), "o link é de um .exe, e o tipo escolhido é msi") {
		t.Errorf("tipo errado:\n%s", out.String())
	}
	for nome, c := range map[string]store.InstallerSource{
		"id":       {PackageID: "../x", Type: "msi"},
		"variável": {PackageID: "x", Type: "msi", URLVar: "$a b"},
		"exe":      {PackageID: "x", Type: "exe"},
	} {
		if err := CheckChoco(c, rule); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}
