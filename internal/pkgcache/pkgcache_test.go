package pkgcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fabricante: um servidor https com um instalador, um que muda, um redirecionamento para
// http, um 404 e um que falha duas vezes antes de responder.
func fabricante(t *testing.T) (*httptest.Server, *Fetcher, []byte, *atomic.Int32) {
	t.Helper()
	instalador := []byte(strings.Repeat("MZ instalador de teste ", 1000))
	var falhas atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/setup.exe":
			w.Header().Set("Content-Length", strconv.Itoa(len(instalador)))
			w.Write(instalador)
		case "/sem-tamanho": // sem Content-Length: o limite vale durante a cópia
			w.(http.Flusher).Flush()
			w.Write(instalador)
		case "/sem-versao.msi": // o endereço é o mesmo; o arquivo, outro
			w.Write(append([]byte("build nova "), instalador...))
		case "/para-http":
			http.Redirect(w, r, "http://exemplo.invalid/setup.exe", http.StatusFound)
		case "/redireciona":
			http.Redirect(w, r, "/setup.exe", http.StatusFound)
		case "/instavel":
			if falhas.Add(1) <= 2 {
				http.Error(w, "ocupado", http.StatusServiceUnavailable)
				return
			}
			w.Write(instalador)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f := NewFetcher("patchd-teste")
	f.HTTP = srv.Client()
	f.HTTP.CheckRedirect = HTTPSOnly
	f.Wait = time.Millisecond
	return srv, f, instalador, &falhas
}

func TestFetch(t *testing.T) {
	srv, f, inst, falhas := fabricante(t)
	dir := t.TempDir()
	ctx := context.Background()
	want := sum(inst)
	var linhas []string
	prog := func(s string) { linhas = append(linhas, s) }

	r, err := f.Fetch(ctx, dir, srv.URL+"/redireciona", want, false, prog)
	if err != nil || r.Cached || r.Size != int64(len(inst)) || r.Path != filepath.Join(dir, want+".exe") {
		t.Fatalf("baixar: %+v %v", r, err)
	}
	// De novo: vem do cache, conferido.
	if r, err := f.Fetch(ctx, dir, srv.URL+"/setup.exe", want, false, prog); err != nil || !r.Cached {
		t.Errorf("cache: %+v %v", r, err)
	}
	// Cache estragado: baixa de novo e avisa.
	os.WriteFile(r.Path, []byte("estragado"), 0o640)
	if r, err := f.Fetch(ctx, dir, srv.URL+"/setup.exe", want, false, prog); err != nil || r.Cached ||
		!strings.Contains(strings.Join(linhas, "\n"), "o arquivo do cache não confere") {
		t.Errorf("cache estragado: %+v %v %v", r, err, linhas)
	}
	if _, err := Check(dir, want, false); err != nil {
		t.Errorf("Check: %v", err)
	}

	// O endereço sem versão entregou outra build: recusado, e nada fica na pasta.
	_, err = f.Fetch(ctx, dir, srv.URL+"/sem-versao.msi", want, true, nil)
	var hm *HashMismatchError
	if !errors.As(err, &hm) || hm.Want != want || hm.Got != sum(append([]byte("build nova "), inst...)) {
		t.Fatalf("hash diferente: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || ents[0].Name() != want+".exe" {
		t.Errorf("a pasta devia ter só o instalador conferido: %v", ents)
	}

	// Repete o que pode melhorar (503), não o que não pode (404, http).
	if _, err := f.Fetch(ctx, t.TempDir(), srv.URL+"/instavel", want, false, prog); err != nil || falhas.Load() != 3 {
		t.Errorf("instável: %v (%d pedidos)", err, falhas.Load())
	}
	for path, msg := range map[string]string{"/nao-existe": "404 Not Found", "/para-http": "fora do https"} {
		falhas.Store(0)
		if _, err := f.Fetch(ctx, t.TempDir(), srv.URL+path, want, false, nil); err == nil || !strings.Contains(err.Error(), msg) ||
			strings.Contains(err.Error(), "tentativa") {
			t.Errorf("%s: %v", path, err)
		}
	}

	// Limite de tamanho: pelo Content-Length, antes de baixar; sem ele, durante a cópia.
	g := *f
	g.MaxSize = 100
	if _, err := g.Fetch(ctx, t.TempDir(), srv.URL+"/setup.exe", want, false, nil); err == nil || !strings.Contains(err.Error(), "o limite é 100 bytes") {
		t.Errorf("limite pelo cabeçalho: %v", err)
	}
	vazia := t.TempDir()
	if _, err := g.Fetch(ctx, vazia, srv.URL+"/sem-tamanho", want, false, nil); err == nil || !strings.Contains(err.Error(), "passou de 100 bytes") {
		t.Errorf("limite na cópia: %v", err)
	}
	if ents, _ := os.ReadDir(vazia); len(ents) != 0 {
		t.Errorf("o temporário ficou: %v", ents)
	}
	// Entradas recusadas antes de qualquer pedido.
	if _, err := f.Fetch(ctx, dir, "http://x/setup.exe", want, false, nil); err == nil || !strings.Contains(err.Error(), "só https") {
		t.Errorf("http: %v", err)
	}
	if _, err := f.Fetch(ctx, dir, srv.URL+"/setup.exe", "abc", false, nil); err == nil {
		t.Error("SHA-256 inválido devia ser recusado")
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{512: "512 bytes", 1700000: "1,6 MiB", 380 << 20: "380,0 MiB", 3 << 30: "3,0 GiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}
