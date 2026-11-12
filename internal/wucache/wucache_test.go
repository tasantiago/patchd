package wucache

import (
	"context"
	"crypto/sha1" // #nosec G505 -- o teste monta o nome como a Microsoft
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	ok := []string{
		"http://download.windowsupdate.com/c/msdownload/update/software/secu/2026/11/windows11.0-kb5070001-x64_0123456789abcdef0123456789abcdef01234567.msu",
		"https://catalog.s.download.windowsupdate.com/d/msdownload/update/software/updt/2026/11/ndp481-kb5070002-x64_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.cab",
		"http://au.download.windowsupdate.com/c/upgr/2026/11/windows11.0-kb5070001-x64.psf",
		"http://tlu.dl.delivery.mp.microsoft.com/filestreamingservice/files/x/windows11.0-kb5070001-x64.cab",
	}
	for _, u := range ok {
		if _, key, err := Parse(u); err != nil || !ValidKey(key) {
			t.Errorf("%s: %q %v", u, key, err)
		}
	}
	// O mesmo caminho em hosts diferentes é o mesmo arquivo.
	_, k1, _ := Parse("http://download.windowsupdate.com/c/a/b.cab")
	_, k2, _ := Parse("https://au.download.windowsupdate.com/c/a/b.cab")
	if k1 != k2 {
		t.Errorf("chaves diferentes para o mesmo caminho: %s %s", k1, k2)
	}
	for _, u := range []string{
		"http://exemplo.com/a.cab", "http://download.windowsupdate.com.exemplo.com/a.cab", "ftp://download.windowsupdate.com/a.cab",
		"http://download.windowsupdate.com/a.txt", "http://download.windowsupdate.com/a.cab?x=1", "http://user@download.windowsupdate.com/a.cab",
		"http://download.windowsupdate.com:8080/a.cab", "http://10.0.0.1/Content/AB/abc.cab", "http://tlu.dl.delivery.mp.microsoft.com/filestreamingservice/files/abc",
	} {
		if _, _, err := Parse(u); err == nil {
			t.Errorf("%s: aceito", u)
		}
	}
	if ValidKey("../x") || ValidKey("0123456789abcdef-../a.cab") || !ValidKey("0123456789abcdef-a.cab") {
		t.Error("ValidKey")
	}
}

func esperaPronto(t *testing.T, c *Cache, urls []string) []Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := c.Ensure(context.Background(), urls)
		pending := false
		for _, s := range st {
			pending = pending || s.State == StateFetching
		}
		if !pending || time.Now().After(deadline) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBaixaUmaVezConfereEServe(t *testing.T) {
	conteudo := []byte(strings.Repeat("atualização cumulativa ", 5000))
	h1 := sha1.Sum(conteudo) // #nosec G401
	nome := "windows11.0-kb5070001-x64_" + hex.EncodeToString(h1[:]) + ".msu"
	var pedidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, nome):
			http.ServeContent(w, r, nome, time.Time{}, strings.NewReader(string(conteudo)))
		case strings.HasSuffix(r.URL.Path, "_0000000000000000000000000000000000000000.cab"):
			_, _ = w.Write([]byte("outro conteúdo"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(t.TempDir(), "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.Origin = func(u *url.URL) []string { return []string{srv.URL + u.Path} }
	bom := "http://download.windowsupdate.com/c/msdownload/" + nome
	errado := "http://download.windowsupdate.com/c/msdownload/x_0000000000000000000000000000000000000000.cab"
	sumido := "http://download.windowsupdate.com/c/msdownload/sumiu.cab"

	st := esperaPronto(t, c, []string{bom, errado, sumido, "http://exemplo.com/a.cab"})
	want := sha256.Sum256(conteudo)
	if st[0].State != StateReady || st[0].Size != int64(len(conteudo)) || st[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("bom: %+v", st[0])
	}
	if st[1].State != StateFailed || !strings.Contains(st[1].Detail, "SHA-1") {
		t.Errorf("SHA-1 errado: %+v", st[1])
	}
	if st[2].State != StateFailed || !strings.Contains(st[2].Detail, "404") {
		t.Errorf("sumido: %+v", st[2])
	}
	if st[3].State != StateRefused {
		t.Errorf("host de fora: %+v", st[3])
	}
	if _, err := os.Stat(c.file(st[1].Key)); err == nil {
		t.Error("o arquivo com SHA-1 errado ficou no disco")
	}
	// Pedido de novo: do disco, sem ir à origem; a falha fica guardada por um tempo.
	antes := pedidos.Load()
	st = c.Ensure(context.Background(), []string{bom, errado})
	if st[0].State != StateReady || st[1].State != StateFailed || pedidos.Load() != antes {
		t.Errorf("de novo: %+v, %d pedidos a mais", st, pedidos.Load()-antes)
	}
	f, m, err := c.Open(st[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != string(conteudo) || m.URL != bom {
		t.Errorf("conteúdo servido: %d bytes, %+v", len(b), m)
	}
	s, err := c.Stats()
	if err != nil || s.Files != 1 || s.Bytes != int64(len(conteudo)) {
		t.Errorf("stats: %+v %v", s, err)
	}
	// A limpeza: o que ninguém pediu desde a data sai.
	if n, bytes, err := c.Prune(time.Now().Add(-time.Hour)); err != nil || n != 0 || bytes != 0 {
		t.Errorf("prune do que foi usado: %d %d %v", n, bytes, err)
	}
	if n, _, err := c.Prune(time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("prune: %d %v", n, err)
	}
	if _, ok := c.Ready(st[0].Key); ok {
		t.Error("depois da limpeza o arquivo continua pronto")
	}
}

func TestRetomaDeOndeParou(t *testing.T) {
	conteudo := []byte(strings.Repeat("x", 100000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a.cab", time.Time{}, strings.NewReader(string(conteudo)))
	}))
	defer srv.Close()
	c, _ := New(t.TempDir(), "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Origin = func(u *url.URL) []string { return []string{srv.URL + u.Path} }
	u := "http://download.windowsupdate.com/c/a.cab"
	_, key, _ := Parse(u)
	if err := os.WriteFile(c.file(key)+".part", conteudo[:40000], 0o600); err != nil {
		t.Fatal(err)
	}
	st := esperaPronto(t, c, []string{u})
	if st[0].State != StateReady || st[0].Size != int64(len(conteudo)) {
		t.Errorf("retomada: %+v", st[0])
	}
}

func TestLimiteDeTamanho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()
	c, _ := New(t.TempDir(), "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Origin = func(u *url.URL) []string { return []string{srv.URL + u.Path} }
	c.MaxSize = 1000
	st := esperaPronto(t, c, []string{"http://download.windowsupdate.com/c/grande.cab"})
	if st[0].State != StateFailed || !strings.Contains(st[0].Detail, "limite") {
		t.Errorf("grande demais: %+v", st[0])
	}
}
