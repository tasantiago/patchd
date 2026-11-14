package agent

import (
	"bytes"
	"context"
	"errors"
	"github.com/tasantiago/patchd/internal/jobs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// O download grande não tem prazo total: um servidor lento mas contínuo passa do prazo do
// cliente dos relatórios; a conexão que cai no meio retoma do ponto onde parou (Range); a
// que fica sem bytes para e retoma; um arquivo trocado no meio não é emendado.
func TestDownloadRetomaENaoTemPrazoTotal(t *testing.T) {
	conteudo := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	mod := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var pedidos atomic.Int32
	var modo atomic.Value
	modo.Store("normal")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := pedidos.Add(1)
		m := modo.Load().(string)
		if n == 1 && r.Header.Get("Range") == "" {
			switch m {
			case "cai", "para", "troca":
				// A primeira resposta manda metade e some.
				w.Header().Set("Last-Modified", mod.Format(http.TimeFormat))
				w.Header().Set("Content-Length", "65536")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(conteudo[:30000])
				w.(http.Flusher).Flush()
				if m == "para" {
					time.Sleep(300 * time.Millisecond) // sem bytes além do prazo de inatividade
				}
				if hj, ok := w.(http.Hijacker); ok {
					c, _, _ := hj.Hijack()
					_ = c.Close()
				}
				return
			case "lento":
				w.Header().Set("Content-Length", "65536")
				w.WriteHeader(http.StatusOK)
				for i := 0; i < len(conteudo); i += 8192 {
					_, _ = w.Write(conteudo[i : i+8192])
					w.(http.Flusher).Flush()
					time.Sleep(40 * time.Millisecond)
				}
				return
			}
		}
		if m == "troca" {
			mod = mod.Add(time.Hour) // o arquivo mudou: o If-Range não confere, vem o 200
		}
		http.ServeContent(w, r, "", mod, bytes.NewReader(conteudo))
	}))
	defer srv.Close()

	for _, caso := range []string{"normal", "cai", "para", "lento", "troca"} {
		t.Run(caso, func(t *testing.T) {
			pedidos.Store(0)
			modo.Store(caso)
			c := NewClient(srv.URL, "cred", "v")
			c.HTTP.Timeout = 150 * time.Millisecond // o prazo total dos relatórios não vale aqui
			c.downloadIdle, c.retryWait = 150*time.Millisecond, time.Millisecond
			var b bytes.Buffer
			n, err := c.Download(context.Background(), "/arquivo", &b, int64(len(conteudo)))
			if caso == "troca" {
				if err == nil || !strings.Contains(err.Error(), "o arquivo mudou no servidor") {
					t.Fatalf("emendou um arquivo trocado: %v", err)
				}
				return
			}
			if err != nil || n != int64(len(conteudo)) || !bytes.Equal(b.Bytes(), conteudo) {
				t.Fatalf("n=%d err=%v igual=%v", n, err, bytes.Equal(b.Bytes(), conteudo))
			}
			quer := int32(1)
			if caso == "cai" || caso == "para" {
				quer = 2
			}
			if p := pedidos.Load(); p != quer {
				t.Errorf("pedidos: %d, esperado %d", p, quer)
			}
		})
	}

	// Limite: a retomada não passa do limite.
	modo.Store("normal")
	c := NewClient(srv.URL, "cred", "v")
	if _, err := c.Download(context.Background(), "/arquivo", &bytes.Buffer{}, 1000); err == nil || !strings.Contains(err.Error(), "maior que o limite") {
		t.Errorf("limite: %v", err)
	}
	// Recusa (404) não repete.
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos.Add(1)
		http.NotFound(w, r)
	}))
	defer srv404.Close()
	pedidos.Store(0)
	var rej *RejectedError
	if _, err := NewClient(srv404.URL, "cred", "v").Download(context.Background(), "/x", &bytes.Buffer{}, 10); !errors.As(err, &rej) || pedidos.Load() != 1 {
		t.Errorf("404: %v, %d pedidos", err, pedidos.Load())
	}
}

// A conexão derrubada antes do primeiro byte (um firewall, um proxy, o servidor reiniciando)
// e o 5xx começam de novo, sem nada escrito; antes, a primeira queda virava falha do job.
// Derrubada sempre, o download desiste depois de downloadAttempts e o erro é da rede
// (jobs.Transient), para o job voltar adiado.
func TestDownloadRepeteAQuedaAntesDoPrimeiroByte(t *testing.T) {
	conteudo := bytes.Repeat([]byte("patchd"), 2000)
	var pedidos, derrubar atomic.Int32
	var modo atomic.Value
	modo.Store("cai")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := pedidos.Add(1); n <= derrubar.Load() {
			if modo.Load() == "503" {
				http.Error(w, "ocupado", http.StatusServiceUnavailable)
				return
			}
			c, _, _ := w.(http.Hijacker).Hijack()
			_ = c.Close() // nenhum byte de resposta
			return
		}
		http.ServeContent(w, r, "", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), bytes.NewReader(conteudo))
	}))
	defer srv.Close()
	baixa := func() (int64, []byte, error) {
		c := NewClient(srv.URL, "cred", "v")
		c.retryWait = time.Millisecond
		var b bytes.Buffer
		n, err := c.Download(context.Background(), "/arquivo", &b, int64(len(conteudo)))
		return n, b.Bytes(), err
	}
	for _, m := range []string{"cai", "503"} {
		modo.Store(m)
		pedidos.Store(0)
		derrubar.Store(3)
		n, got, err := baixa()
		if err != nil || n != int64(len(conteudo)) || !bytes.Equal(got, conteudo) || pedidos.Load() < 4 {
			t.Errorf("%s 3 vezes e depois atende: n=%d err=%v pedidos=%d", m, n, err, pedidos.Load())
		}
	}
	modo.Store("cai")
	pedidos.Store(0)
	derrubar.Store(1000)
	n, got, err := baixa()
	var te *TransientError
	if !errors.As(err, &te) || !jobs.Transient(err) || n != 0 || len(got) != 0 || pedidos.Load() < downloadAttempts ||
		pedidos.Load() > 2*downloadAttempts { // o transporte do Go repete sozinho a conexão reaproveitada que cai
		t.Errorf("sempre derrubada: n=%d err=%v pedidos=%d (esperava de %d a %d)", n, err, pedidos.Load(), downloadAttempts, 2*downloadAttempts)
	}
	// O erro que não é da rede não é "transitório".
	if jobs.Transient(&RejectedError{Status: 404}) || jobs.Transient(errors.New("hash diferente")) || jobs.Transient(nil) {
		t.Error("recusa e erro comum não são da rede")
	}
}
