package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 0

// requestIDPattern: IDs vindos do cliente só são aceitos neste formato (evita lixo no log).
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID devolve o ID da requisição guardado no contexto.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withRequestID usa o X-Request-ID do cliente (se válido) ou gera um, devolve-o na
// resposta e o coloca no contexto: é o que casa o log do agente com o do servidor.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// statusRecorder guarda o status e os bytes escritos, para o log de acesso.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// withAccessLog registra cada requisição: método, caminho, status, tamanho, duração e ID.
func withAccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		logger.Info("requisição",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"request_bytes", r.ContentLength,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestID(r.Context()),
			"remote", r.RemoteAddr,
		)
	})
}

// withRecover transforma um pânico no tratamento de uma requisição num 500 registrado,
// em vez de derrubar o servidor. http.ErrAbortHandler é o aborto intencional do net/http.
func withRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			logger.Error("pânico ao tratar requisição",
				"panic", fmt.Sprint(v),
				"stack", string(debug.Stack()),
				"request_id", RequestID(r.Context()),
			)
			writeError(w, http.StatusInternalServerError, "internal", "erro interno do servidor")
		}()
		next.ServeHTTP(w, r)
	})
}
