package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Downloads (instaladores de terceiros, bundles de scripts, versões do agente): um instalador
// de centenas de MB numa rede lenta (Wi-Fi, VPN, relé) leva bem mais que o prazo total de 2
// minutos do cliente dos relatórios. Aqui não há prazo total: vale o tempo sem nenhum byte
// (downloadIdle), e uma conexão que cai no meio retoma do ponto onde parou (Range), até
// downloadAttempts vezes. A que cai antes do primeiro byte (a conexão recusada ou derrubada
// por um firewall, um proxy ou o servidor reiniciando, um 5xx) começa de novo: nada foi
// escrito ainda. Quem chama confere o tamanho e o SHA-256 do que chegou.
const (
	downloadIdle       = 2 * time.Minute // sem nenhum byte por esse tempo, a tentativa para
	downloadHeaderWait = time.Minute     // até a resposta começar
	downloadAttempts   = 5
)

// Download baixa um arquivo (GET, com a credencial da máquina) para w. Recusa respostas
// maiores que limit: um servidor comprometido não consegue encher o disco da máquina.
func (c *Client) Download(ctx context.Context, path string, w io.Writer, limit int64) (int64, error) {
	var (
		n       int64
		version string // Last-Modified (ou ETag) da primeira resposta: a retomada é do mesmo arquivo
		err     error
	)
	wait := c.retryWait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	for attempt := 1; ; attempt++ {
		var got int64
		got, version, err = c.downloadFrom(ctx, path, w, limit, n, version)
		n += got
		var te *TransientError
		// Com bytes já escritos, só dá para retomar sabendo que o arquivo é o mesmo (version).
		// Sem nenhum, a tentativa seguinte começa do zero. Antes, a queda antes do primeiro
		// byte não era repetida, e uma conexão derrubada virava "o instalador não chegou a
		// rodar" na primeira vez.
		if err == nil || !errors.As(err, &te) || attempt >= downloadAttempts || ctx.Err() != nil || (n > 0 && version == "") {
			return n, err
		}
		// A espera varia de máquina para máquina: numa campanha, as que caíram juntas não
		// voltam todas no mesmo instante.
		// #nosec G404 -- espalhar as tentativas não precisa de aleatório criptográfico
		t := time.NewTimer(wait + time.Duration(rand.Int64N(int64(wait)/2+1)))
		select {
		case <-ctx.Done():
			t.Stop()
			return n, ctx.Err()
		case <-t.C:
		}
		wait *= 2
	}
}

// downloadFrom faz uma tentativa a partir de offset; devolve os bytes escritos nela.
func (c *Client) downloadFrom(ctx context.Context, path string, w io.Writer, limit, offset int64, version string) (int64, string, error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, version, err
	}
	requestID := newRequestID()
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "patchd-agent/"+c.AgentVersion)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", version)
	}
	// O mesmo transporte (TLS, CA interna, relé), sem o prazo total do cliente.
	hc := &http.Client{Transport: c.HTTP.Transport, CheckRedirect: c.HTTP.CheckRedirect, Jar: c.HTTP.Jar}
	headerWait := time.AfterFunc(downloadHeaderWait, cancel)
	resp, err := hc.Do(req)
	headerWait.Stop()
	if err != nil {
		if ctx.Err() != nil {
			return 0, version, ctx.Err()
		}
		return 0, version, &TransientError{Err: err, RequestID: requestID}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK && offset == 0:
		version = resp.Header.Get("ETag")
		if version == "" {
			version = resp.Header.Get("Last-Modified")
		}
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		if start := contentRangeStart(resp.Header.Get("Content-Range")); start != offset {
			return 0, version, fmt.Errorf("Content-Range começa em %d, esperado %d (request_id %s)", start, offset, requestID)
		}
	case resp.StatusCode == http.StatusOK:
		// O arquivo mudou no servidor desde a primeira parte: não dá para emendar.
		return 0, version, fmt.Errorf("o arquivo mudou no servidor durante o download (request_id %s)", requestID)
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, version, ErrUnauthorized
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return 0, version, &TransientError{Err: fmt.Errorf("o servidor respondeu %d", resp.StatusCode), RequestID: requestID}
	default:
		var e protocol.ErrorResponse
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = json.Unmarshal(data, &e)
		return 0, version, &RejectedError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RequestID: requestID}
	}
	idle := c.downloadIdle
	if idle <= 0 {
		idle = downloadIdle
	}
	body := newStallReader(resp.Body, idle, cancel)
	n, err := io.Copy(w, io.LimitReader(body, limit-offset+1))
	body.stop()
	switch {
	case body.stalled():
		return n, version, &TransientError{Err: fmt.Errorf("nenhum byte em %s, com %s baixados", idle, sizeMB(offset+n)), RequestID: requestID}
	case err != nil && ctx.Err() != nil:
		return n, version, ctx.Err()
	case err != nil:
		return n, version, &TransientError{Err: err, RequestID: requestID}
	case offset+n > limit:
		return n, version, fmt.Errorf("%s: resposta maior que o limite de %d bytes", path, limit)
	}
	return n, version, nil
}

func sizeMB(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }
