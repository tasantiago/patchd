package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Uma resposta lenta, mais longa que o WriteTimeout do servidor: sem ExtendWrites, a
// conexão é cortada no prazo; com ele, a resposta chega inteira.
func TestExtendWrites(t *testing.T) {
	const partes, pausa = 8, 80 * time.Millisecond // 640 ms no total
	lenta := func(estender bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if estender {
				w = ExtendWrites(w, 500*time.Millisecond)
			}
			w.Header().Set("Content-Length", "8")
			for range partes {
				if _, err := w.Write([]byte("x")); err != nil {
					return
				}
				_ = http.NewResponseController(w).Flush()
				time.Sleep(pausa)
			}
		}
	}
	for _, c := range []struct {
		estender bool
		inteira  bool
	}{{false, false}, {true, true}} {
		srv := httptest.NewUnstartedServer(lenta(c.estender))
		srv.Config.WriteTimeout = 250 * time.Millisecond
		srv.Start()
		resp, err := http.Get(srv.URL)
		var n int
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			n = len(b)
		}
		srv.Close()
		if got := n == partes; got != c.inteira {
			t.Errorf("estender=%v: %d de %d bytes (err %v)", c.estender, n, partes, err)
		}
	}
}
