package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/httpx"
)

func TestRecuperaPanicoComoErro500(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	quebra := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("defeito de teste") })
	h := withRequestID(withAccessLog(logger, withRecover(logger, quebra)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"internal"`) {
		t.Errorf("pânico deveria virar 500 em JSON: %d %s", rec.Code, rec.Body)
	}
}

func TestRequestIDDoClienteValidoEReaproveitado(t *testing.T) {
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, RequestID(r.Context()))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "agente-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "agente-123" || rec.Header().Get("X-Request-ID") != "agente-123" {
		t.Errorf("ID válido do cliente deveria ser mantido: %q", rec.Body.String())
	}

	req.Header.Set("X-Request-ID", "lixo com espaço\n")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "lixo") || len(rec.Body.String()) != 16 {
		t.Errorf("ID inválido deveria ser trocado por um gerado: %q", rec.Body.String())
	}
}

// O prazo de escrita estendido (downloads grandes) atravessa o registro de acesso: sem o
// Unwrap do statusRecorder, o http.ResponseController não alcançaria a conexão e o
// WriteTimeout do servidor cortaria a resposta.
func TestPrazoDeEscritaAtravessaOLogDeAcesso(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lenta := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = httpx.ExtendWrites(w, 500*time.Millisecond)
		w.Header().Set("Content-Length", "6")
		for range 6 {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			_ = http.NewResponseController(w).Flush()
			time.Sleep(80 * time.Millisecond)
		}
	})
	srv := httptest.NewUnstartedServer(withRequestID(withAccessLog(logger, lenta)))
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(b) != 6 {
		t.Errorf("resposta cortada: %d bytes, %v", len(b), err)
	}
}
