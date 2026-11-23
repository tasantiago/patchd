package main

import (
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
