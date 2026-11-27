package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// O download automático: o programa com "baixar sozinho" e a fonte do fabricante baixa a
// versão de referência nova uma vez; a mesma versão não é baixada de novo, e a falha só é
// tentada de novo depois do intervalo.
func TestDownloadAutomatico(t *testing.T) {
	inst := authsigtest.Exe("Mozilla Corporation")
	sum := sha256.Sum256(inst)
	pedidos := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/firefox/147.0/SHA256SUMS":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  win64/pt-BR/Firefox Setup 147.0.exe\n"))
		case "/firefox/147.0/win64/pt-BR/Firefox Setup 147.0.exe":
			pedidos++
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m, _ := thirdparty.Default()
	st := store.NewMemory()
	ctx := context.Background()
	st.SaveThirdPartyVersion(ctx, "mozilla-firefox", "windows", "147.0", "firefox:LATEST_FIREFOX_VERSION")
	st.SaveThirdPartyVersion(ctx, "7zip", "windows", "26.06", "winget:7zip.7zip")
	st.SaveInstallerSource(ctx, store.InstallerSource{AppID: "mozilla-firefox", Kind: store.SourceURL, Type: "nullsoft", AutoFetch: true, UpdatedBy: "ana",
		URLTemplate: srv.URL + "/firefox/{versao}/win64/pt-BR/Firefox%20Setup%20{versao}.exe", ChecksumURL: srv.URL + "/firefox/{versao}/SHA256SUMS"})
	st.SaveInstallerSource(ctx, store.InstallerSource{AppID: "7zip", Kind: store.SourceURL, Type: "exe", Arguments: "/S", AutoFetch: false, UpdatedBy: "ana",
		URLTemplate: srv.URL + "/7zip/{versao}.exe", ChecksumURL: srv.URL + "/x"})
	sf := newSourceFetcher(st, t.TempDir(), "teste")
	sf.f.HTTP = srv.Client()
	sf.f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	sf.f.Wait = time.Millisecond
	sf.hc = srv.Client()
	agora := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	af := &autoFetch{s: sf, rules: func() thirdparty.Manifest { return m }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time { return agora }}
	if n := af.pass(ctx); n != 1 || pedidos != 1 {
		t.Fatalf("primeira passada: %d tentativa(s), %d download(s)", n, pedidos)
	}
	pk, _ := st.ThirdPartyPackages(ctx)
	if len(pk) != 1 || pk[0].AppID != "mozilla-firefox" || pk[0].Version != "147.0" || pk[0].Type != "nullsoft" {
		t.Fatalf("pacotes: %+v", pk)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "terceiros.baixar"}); len(ev) != 1 || ev[0].Username != "patchd" ||
		!strings.Contains(ev[0].Detail, "automático: versão de referência 147.0, pelo site do fabricante: baixado") {
		t.Errorf("auditoria: %+v", ev)
	}
	// Já guardada: nada.
	if n := af.pass(ctx); n != 0 {
		t.Errorf("segunda passada: %d", n)
	}
	// Versão nova que o fabricante ainda não publicou: tenta, falha, e só tenta de novo depois do intervalo.
	st.SaveThirdPartyVersion(ctx, "mozilla-firefox", "windows", "148.0", "firefox:LATEST_FIREFOX_VERSION")
	if n := af.pass(ctx); n != 1 {
		t.Errorf("148.0: %d", n)
	}
	agora = agora.Add(time.Hour)
	if n := af.pass(ctx); n != 0 {
		t.Errorf("148.0 de novo antes do intervalo: %d", n)
	}
	agora = agora.Add(autoRetry)
	if n := af.pass(ctx); n != 1 {
		t.Errorf("148.0 depois do intervalo: %d", n)
	}
}

func TestTemVersao(t *testing.T) {
	pk := []store.ThirdPartyPackage{{AppID: "ff", Version: "140.5.0esr"}, {AppID: "x", Version: "1.0"}}
	if !hasVersion(pk, "ff", "140.5.0esr") || !hasVersion(pk, "ff", "140.5.0") || hasVersion(pk, "ff", "140.6.0") || hasVersion(pk, "y", "1.0") {
		t.Error("hasVersion")
	}
}

// O programa incluído do Chocolatey: sem fonte gravada, o Baixar usa o pacote da regra (com o
// tipo e os argumentos dela); com "baixar sozinho" (a linha winget só com o automático), também.
func TestFonteDoProgramaDoChocolatey(t *testing.T) {
	inst := authsigtest.Exe("Notepad++")
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	var srvURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/package/notepadplusplus.install":
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			fw, _ := zw.Create("notepadplusplus.install.nuspec")
			fw.Write([]byte(`<package><metadata><id>notepadplusplus.install</id><version>8.9.8.20261101</version></metadata></package>`))
			fw, _ = zw.Create("tools/chocolateyInstall.ps1")
			fw.Write([]byte("$packageArgs = @{\n  url64bit = '" + srvURL + "/npp.8.9.8.Installer.x64.exe'\n  checksum64 = '" + sha + "'\n}\n"))
			zw.Close()
			w.Write(b.Bytes())
		case "/npp.8.9.8.Installer.x64.exe":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	rule, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Notepad++", Chocolatey: "notepadplusplus.install", InventoryName: "Notepad++",
		NameMode: thirdparty.NamePrefix, InstallType: "nullsoft", Signer: "Notepad++"})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	ctx := context.Background()
	sf := newSourceFetcher(st, t.TempDir(), "teste")
	sf.f.HTTP = srv.Client()
	sf.f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	sf.f.Wait = time.Millisecond
	sf.hc, sf.choco = srv.Client(), srv.URL+"/api/v2"
	var out strings.Builder
	if !sf.fetch(ctx, rule, "", nil, &out) {
		t.Fatalf("baixar:\n%s", out.String())
	}
	pk, _ := st.ThirdPartyPackages(ctx)
	if len(pk) != 1 || pk[0].AppID != rule.ID || pk[0].Version != "8.9.8" || pk[0].Type != "nullsoft" || pk[0].WingetID != "chocolatey:notepadplusplus.install" {
		t.Fatalf("pacotes: %+v\n%s", pk, out.String())
	}
	// O automático: a versão de referência nova (a do pacote, que o servidor ainda não tem).
	st.SaveThirdPartyVersion(ctx, rule.ID, "windows", "8.9.9", "chocolatey:notepadplusplus.install")
	st.SaveInstallerSource(ctx, store.InstallerSource{AppID: rule.ID, Kind: store.SourceWinget, AutoFetch: true, UpdatedBy: "ana"})
	m := thirdparty.Manifest{Version: 1, Apps: []thirdparty.App{rule}}
	var log bytes.Buffer
	af := &autoFetch{s: sf, rules: func() thirdparty.Manifest { return m }, logger: slog.New(slog.NewTextHandler(&log, nil)),
		now: time.Now}
	if n := af.pass(ctx); n != 1 || !strings.Contains(log.String(), "fonte=Chocolatey") {
		t.Errorf("automático: %d\n%s", n, log.String())
	}
}

// O programa do site do fabricante com a versão manual: sem versão no Baixar e sem
// referência, pede a versão; com a versão digitada, baixa e ela vira a de referência; sem o
// link gravado, explica onde gravar.
func TestFabricanteComVersaoManual(t *testing.T) {
	inst := authsigtest.Exe("Exemplo Ltda")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sistema/5.2.1/sistema.exe" || r.URL.Path == "/sistema/5.1/sistema.exe" {
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	rule, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Sistema Exemplo", Vendor: true, InventoryName: "Sistema Exemplo", InstallType: "exe",
		Arguments: "/S", Signer: "Exemplo Ltda"})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	ctx := context.Background()
	sf := newSourceFetcher(st, t.TempDir(), "teste")
	sf.f.HTTP = srv.Client()
	sf.f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	sf.f.Wait = time.Millisecond
	sf.hc = srv.Client()
	var out strings.Builder
	if sf.fetch(ctx, rule, "", nil, &out) || !strings.Contains(out.String(), "sem o link do instalador") {
		t.Fatalf("sem o link: %s", out.String())
	}
	st.SaveInstallerSource(ctx, store.InstallerSource{AppID: rule.ID, Kind: store.SourceURL, Type: "exe", Arguments: "/S", UpdatedBy: "ana",
		URLTemplate: srv.URL + "/sistema/{versao}/sistema.exe"})
	out.Reset()
	if sf.fetch(ctx, rule, "", nil, &out) || !strings.Contains(out.String(), "digite a versão no Baixar") {
		t.Fatalf("sem versão: %s", out.String())
	}
	ref := func() string {
		refs, _ := st.ThirdPartyVersions(ctx)
		for _, r := range refs {
			if r.App == rule.ID {
				return r.Version
			}
		}
		return ""
	}
	for _, ver := range []string{"5.2.1", "5.1"} { // a mais nova fica, mesmo baixando uma antiga depois
		out.Reset()
		if !sf.fetch(ctx, rule, ver, nil, &out) {
			t.Fatalf("baixar %s: %s", ver, out.String())
		}
	}
	if got := ref(); got != "5.2.1" {
		t.Fatalf("referência: %q", got)
	}
	// Vazio agora: a de referência.
	out.Reset()
	if !sf.fetch(ctx, rule, "", nil, &out) || !strings.Contains(out.String(), "5.2.1") {
		t.Fatalf("vazio com referência: %s", out.String())
	}
}

// A fonte Evergreen: o servidor lê as linhas do aplicativo, escolhe a do filtro e do tipo e
// baixa o link dela, conferindo o SHA-256 que ele informa (o arquivo diferente é recusado).
func TestFonteEvergreen(t *testing.T) {
	inst := authsigtest.Exe("Exemplo Ltda")
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	var srvURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/ExemploApp":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Version":"3.1.0","Architecture":"x64","Type":"exe","Channel":"Stable","URI":"` + srvURL + `/dl/exemplo-3.1.0-x64.exe","Sha256":"` + sha + `"},
				{"Version":"3.1.0","Architecture":"arm64","Type":"exe","Channel":"Stable","URI":"` + srvURL + `/dl/exemplo-3.1.0-arm64.exe","Sha256":"` + strings.Repeat("0", 64) + `"},
				{"Version":"3.2.0","Architecture":"x64","Type":"exe","Channel":"Beta","URI":"` + srvURL + `/dl/beta.exe"}]`))
		case "/dl/exemplo-3.1.0-x64.exe", "/dl/exemplo-3.1.0-arm64.exe":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(inst))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	rule, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Exemplo", Vendor: true, InventoryName: "Exemplo", InstallType: "nullsoft",
		Evergreen: "ExemploApp", EvergreenFilter: map[string]string{"Channel": "Stable"}, UnsignedReason: "o Evergreen dá o SHA-256"})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	ctx := context.Background()
	sf := newSourceFetcher(st, t.TempDir(), "teste")
	sf.f.HTTP = srv.Client()
	sf.f.HTTP.CheckRedirect = pkgcache.HTTPSOnly
	sf.f.Wait = time.Millisecond
	sf.eg.HTTP, sf.eg.EvergreenURL = srv.Client(), srv.URL
	if err := st.SaveInstallerSource(ctx, store.InstallerSource{AppID: rule.ID, Kind: store.SourceEvergreen, PackageID: "ExemploApp", Filter: "Channel=Stable",
		Type: "nullsoft", UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if !sf.fetch(ctx, rule, "", nil, &out) || !strings.Contains(out.String(), "(do Evergreen)") {
		t.Fatalf("baixar: %s", out.String())
	}
	pk, _ := st.ThirdPartyPackages(ctx)
	if len(pk) != 1 || pk[0].Version != "3.1.0" || pk[0].SHA256 != sha || !strings.HasSuffix(pk[0].URL, "-x64.exe") || pk[0].WingetID != "evergreen" {
		t.Fatalf("pacote: %+v", pk)
	}
	// O beta não passa no filtro; a versão que ele não lista é recusada com o que ele tem.
	out.Reset()
	if sf.fetch(ctx, rule, "3.2.0", nil, &out) || !strings.Contains(out.String(), "tem: 3.1.0") {
		t.Fatalf("versão fora do filtro: %s", out.String())
	}
	// O SHA-256 que não confere: recusado (a linha arm64, escolhida pelo filtro).
	st.SaveInstallerSource(ctx, store.InstallerSource{AppID: rule.ID, Kind: store.SourceEvergreen, PackageID: "ExemploApp", Filter: "Architecture=arm64",
		Type: "nullsoft", UpdatedBy: "ana"})
	out.Reset()
	if sf.fetch(ctx, rule, "", nil, &out) || !strings.Contains(out.String(), "RECUSADO") {
		t.Fatalf("SHA-256 diferente: %s", out.String())
	}
}
