// Package retry repete pedidos GET que falham por motivo passageiro: erro de rede (inclusive
// a conexão que cai no meio da resposta), 429 (limite de pedidos) e 5xx (Aula 6.6).
//
// A resposta bem-sucedida é lida inteira dentro do Transport: assim, um "connection reset by
// peer" no meio do corpo também é repetido, e quem chamou recebe o corpo completo ou um erro,
// nunca metade. O Bodhi tem a própria repetição (Aula 6.2) e não usa este pacote.
package retry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Padrões: quatro tentativas, esperando 5 s, 10 s e 15 s entre elas (30 s no total, dentro do
// timeout dos clientes do catálogo).
const (
	DefaultAttempts = 4
	DefaultWait     = 5 * time.Second
	DefaultMaxBody  = 512 << 20 // acima disso, o corpo segue em fluxo, sem leitura antecipada
	maxRetryAfter   = time.Minute
)

// Transport é um http.RoundTripper com repetição.
type Transport struct {
	Base     http.RoundTripper // nil: http.DefaultTransport
	Attempts int               // 0: DefaultAttempts
	Wait     time.Duration     // espera antes da tentativa n+1: n×Wait (0: DefaultWait)
	MaxBody  int64             // 0: DefaultMaxBody
	// OnRetry, quando definido, é chamado antes de cada repetição.
	OnRetry func(req *http.Request, attempt int, reason string)
}

// Wrap devolve o cliente com o transporte dele envolvido pela repetição.
func Wrap(hc *http.Client, onRetry func(*http.Request, int, string)) *http.Client {
	if hc == nil {
		hc = &http.Client{}
	}
	hc.Transport = &Transport{Base: hc.Transport, OnRetry: onRetry}
	return hc
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return base.RoundTrip(req) // só pedidos idempotentes e sem corpo são repetidos
	}
	attempts, wait, maxBody := t.Attempts, t.Wait, t.MaxBody
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	if wait <= 0 {
		wait = DefaultWait
	}
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}

	for attempt := 1; ; attempt++ {
		resp, err := base.RoundTrip(req)
		reason, delay := "", time.Duration(attempt)*wait
		switch {
		case err != nil:
			reason = err.Error()
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			reason = resp.Status
			if d, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				delay = d
			}
			if attempt == attempts {
				return resp, nil // a última resposta volta como veio, para a mensagem de erro de quem chamou
			}
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		case resp.StatusCode >= 200 && resp.StatusCode < 300 && req.Method == http.MethodGet:
			if resp.ContentLength > maxBody {
				return resp, nil // grande demais para a memória: segue em fluxo, sem repetição do corpo
			}
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
			switch {
			case rerr != nil:
				resp.Body.Close()
				reason = "corpo interrompido: " + rerr.Error()
				err = rerr
			case int64(len(data)) > maxBody:
				// Sem Content-Length e maior que o limite: o que já foi lido volta na frente
				// do resto, em fluxo (a Aula 6.6, parte 2, devolvia erro aqui).
				resp.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(data), resp.Body), resp.Body}
				return resp, nil
			default:
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(data))
				resp.ContentLength = int64(len(data))
				return resp, nil
			}
		default:
			return resp, nil // 3xx, 4xx: não melhora repetindo
		}
		if attempt == attempts || req.Context().Err() != nil {
			if err != nil {
				return nil, fmt.Errorf("%w (depois de %d tentativa(s))", err, attempt)
			}
			return nil, fmt.Errorf("%s: %s (depois de %d tentativa(s))", req.URL.Redacted(), reason, attempt)
		}
		if t.OnRetry != nil {
			t.OnRetry(req, attempt+1, reason)
		}
		if err := sleep(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// retryAfter lê o Retry-After em segundos (a forma com data não é usada por estas fontes).
func retryAfter(v string) (time.Duration, bool) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return min(time.Duration(n)*time.Second, maxRetryAfter), true
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
