package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Download baixa um arquivo (GET, com a credencial da máquina) para w. Recusa respostas
// maiores que limit: um servidor comprometido não consegue encher o disco da máquina.
func (c *Client) Download(ctx context.Context, path string, w io.Writer, limit int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, err
	}
	requestID := newRequestID()
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "patchd-agent/"+c.AgentVersion)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &TransientError{Err: err, RequestID: requestID}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, ErrUnauthorized
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return 0, &TransientError{Err: fmt.Errorf("o servidor respondeu %d", resp.StatusCode), RequestID: requestID}
	default:
		var e protocol.ErrorResponse
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = json.Unmarshal(data, &e)
		return 0, &RejectedError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RequestID: requestID}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, &TransientError{Err: err, RequestID: requestID}
	}
	if n > limit {
		return n, fmt.Errorf("%s: resposta maior que o limite de %d bytes", path, limit)
	}
	return n, nil
}
