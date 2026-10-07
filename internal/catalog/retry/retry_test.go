package retry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func cliente(t *testing.T, repeticoes *[]string) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 10 * time.Second, Transport: &Transport{Wait: time.Millisecond,
		OnRetry: func(r *http.Request, n int, motivo string) { *repeticoes = append(*repeticoes, motivo) }}}
}

func TestRepeteEDesiste(t *testing.T) {
	var pedidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := pedidos.Add(1)
		switch r.URL.Path {
		case "/instavel":
			// 503, 429 (com Retry-After 0) e depois o corpo.
			switch n {
			case 1:
				http.Error(w, "fora", http.StatusServiceUnavailable)
			case 2:
				w.Header().Set("Retry-After", "0")
				http.Error(w, "devagar", http.StatusTooManyRequests)
			default:
				w.Write([]byte("conteúdo completo"))
			}
		case "/corte":
			// Promete 1000 bytes e derruba a conexão no meio, nas duas primeiras vezes.
			if n <= 2 {
				w.Header().Set("Content-Length", "1000")
				w.Write([]byte("metade"))
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			w.Write([]byte("inteiro"))
		case "/sempre-500":
			http.Error(w, "quebrado", http.StatusInternalServerError)
		case "/404":
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	casos := []struct {
		caminho     string
		corpo       string
		status      int
		pedidos     int32
		repeticoes  int
		erroContido string
	}{
		{"/instavel", "conteúdo completo", 200, 3, 2, ""},
		{"/corte", "inteiro", 200, 3, 2, ""},
		{"/sempre-500", "", 500, 4, 3, ""}, // a última resposta volta como veio
		{"/404", "", 404, 1, 0, ""},        // 4xx não é repetido
	}
	for _, c := range casos {
		pedidos.Store(0)
		var rep []string
		resp, err := cliente(t, &rep).Get(srv.URL + c.caminho)
		if err != nil {
			t.Errorf("%s: %v", c.caminho, err)
			continue
		}
		corpo, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || (c.corpo != "" && string(corpo) != c.corpo) || pedidos.Load() != c.pedidos || len(rep) != c.repeticoes {
			t.Errorf("%s: status %d, corpo %q, %d pedidos, repetições %q", c.caminho, resp.StatusCode, corpo, pedidos.Load(), rep)
		}
	}
}

func TestErroDeRedeERequisicaoNaoRepetivel(t *testing.T) {
	// Endereço que recusa a conexão: quatro tentativas e o erro com a contagem.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	var rep []string
	_, err := cliente(t, &rep).Get(url)
	if err == nil || !strings.Contains(err.Error(), "depois de 4 tentativa(s)") || len(rep) != 3 {
		t.Errorf("rede: %v, repetições %q", err, rep)
	}

	// POST não é repetido.
	var pedidos atomic.Int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos.Add(1)
		http.Error(w, "fora", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	resp, err := cliente(t, &rep).Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err != nil || resp.StatusCode != 503 || pedidos.Load() != 1 {
		t.Errorf("POST: %v, %d pedidos", err, pedidos.Load())
	}
}

func TestCancelamentoInterrompeAEspera(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fora", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	hc := &http.Client{Transport: &Transport{Wait: time.Hour}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	inicio := time.Now()
	if _, err := hc.Do(req); err == nil || time.Since(inicio) > 5*time.Second {
		t.Errorf("cancelamento: %v depois de %v", err, time.Since(inicio))
	}
}

func TestRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"0": 0, "7": 7 * time.Second, "3600": time.Minute} {
		if got, ok := retryAfter(in); !ok || got != want {
			t.Errorf("%q: %v %t", in, got, ok)
		}
	}
	for _, in := range []string{"", "-1", "Wed, 21 Oct 2026 07:28:00 GMT"} {
		if _, ok := retryAfter(in); ok {
			t.Errorf("%q deveria ser ignorado", in)
		}
	}
}

func TestCorpoGrandeSegueEmFluxo(t *testing.T) {
	grande := strings.Repeat("x", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/com-tamanho" {
			w.Header().Set("Content-Length", strconv.Itoa(len(grande)))
		} else {
			w.(http.Flusher).Flush() // sem Content-Length: corpo em pedaços
		}
		w.Write([]byte(grande))
	}))
	defer srv.Close()
	hc := &http.Client{Transport: &Transport{MaxBody: 1000, Wait: time.Millisecond}}
	for _, caminho := range []string{"/com-tamanho", "/sem-tamanho"} {
		resp, err := hc.Get(srv.URL + caminho)
		if err != nil {
			t.Fatalf("%s: %v", caminho, err)
		}
		corpo, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(corpo) != grande {
			t.Errorf("%s: %d bytes, %v", caminho, len(corpo), err)
		}
	}
}
