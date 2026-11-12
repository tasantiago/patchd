package agent

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wucache"
)

// servidorDeCache: o lado do servidor do cache do Windows, de mentira. prontoEm: em qual
// pergunta o arquivo fica pronto; estado: o estado antes disso.
func servidorDeCache(t *testing.T, conteudo string, prontoEm int32, estado, anunciado string) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	sum := sha256.Sum256([]byte(conteudo))
	if anunciado == "" {
		anunciado = hex.EncodeToString(sum[:])
	}
	var perguntas, downloads atomic.Int32
	const key = "0123456789abcdef-windows11.0-kb5070001-x64.msu"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == protocol.WinCachePath:
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			var req protocol.WinCacheRequest
			_ = json.NewDecoder(zr).Decode(&req)
			n := perguntas.Add(1)
			resp := protocol.WinCacheResponse{}
			for _, u := range req.URLs {
				f := protocol.WinCacheFile{URL: u, Key: key, State: estado, Size: int64(len(conteudo)), Done: 10, Detail: "a Microsoft respondeu 404"}
				if n >= prontoEm {
					f.State, f.SHA256 = wucache.StateReady, anunciado
				}
				resp.Files = append(resp.Files, f)
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && r.URL.Path == protocol.WinCachePath+"/"+key:
			downloads.Add(1)
			w.Header().Set("Last-Modified", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat))
			http.ServeContent(w, r, key, time.Time{}, strings.NewReader(conteudo))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &perguntas, &downloads
}

func TestCacheDoWindowsNoAgente(t *testing.T) {
	const u = "http://download.windowsupdate.com/c/msdownload/windows11.0-kb5070001-x64.msu"
	conteudo := strings.Repeat("pacote ", 20000)
	srv, perguntas, downloads := servidorDeCache(t, conteudo, 3, wucache.StateFetching, "")
	defer srv.Close()
	dir := t.TempDir()
	f := WinCacheFetcher{Client: NewClient(srv.URL, "cred", "teste"), Dir: dir, Poll: time.Millisecond}

	// Espera o servidor baixar (duas perguntas "baixando"), baixa e confere.
	got, err := f.Fetch(context.Background(), []string{u, u})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got[u])
	if string(b) != conteudo || perguntas.Load() != 3 || downloads.Load() != 1 {
		t.Fatalf("arquivo: %d bytes, %d perguntas, %d downloads", len(b), perguntas.Load(), downloads.Load())
	}
	// De novo: o arquivo da última vez é reaproveitado, sem baixar.
	if _, err := f.Fetch(context.Background(), []string{u}); err != nil || downloads.Load() != 1 {
		t.Errorf("reaproveitar: %v, %d downloads", err, downloads.Load())
	}
}

func TestCacheDoWindowsNoAgenteFalhas(t *testing.T) {
	const u = "http://download.windowsupdate.com/c/a.cab"
	conteudo := strings.Repeat("x", 5000)
	casos := []struct {
		nome      string
		prontoEm  int32
		estado    string
		anunciado string
		maxWait   time.Duration
		quer      string
		alvo      error
	}{
		{"servidor ainda baixando: adia", 1000, wucache.StateFetching, "", 20 * time.Millisecond, "ainda está baixando", jobs.ErrRetry},
		{"servidor recusou o endereço", 1000, wucache.StateRefused, "", time.Second, "não guarda", jobs.ErrRefused},
		{"servidor não conseguiu baixar", 1000, wucache.StateFailed, "", time.Second, "404", nil},
		{"conteúdo diferente do anunciado", 1, wucache.StateFetching, strings.Repeat("0", 64), time.Second, "não é o anunciado", nil},
	}
	for _, c := range casos {
		srv, _, _ := servidorDeCache(t, conteudo, c.prontoEm, c.estado, c.anunciado)
		dir := t.TempDir()
		f := WinCacheFetcher{Client: NewClient(srv.URL, "cred", "teste"), Dir: dir, Poll: time.Millisecond, MaxWait: c.maxWait}
		_, err := f.Fetch(context.Background(), []string{u})
		if err == nil || !strings.Contains(err.Error(), c.quer) || (c.alvo != nil && !errors.Is(err, c.alvo)) {
			t.Errorf("%s: %v", c.nome, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: sobrou arquivo na máquina: %d", c.nome, len(entries))
		}
		srv.Close()
	}
}
