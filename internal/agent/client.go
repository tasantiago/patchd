// Package agent é o laço de check-in do patchd-agent (Aula 4.4): fala com o servidor
// usando a credencial do enrollment, decide o que enviar, guarda o que não pôde ser
// enviado e escolhe quando tentar de novo.
package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	httpTimeout     = 2 * time.Minute // um inventário grande numa rede lenta
	maxResponseSize = 1 << 20
)

// ErrUnauthorized: o servidor recusou a credencial (revogada, banco restaurado, servidor
// errado). Repetir não adianta: alguém precisa registrar a máquina de novo.
var ErrUnauthorized = errors.New("o servidor recusou a credencial da máquina")

// RejectedError: o servidor recusou o conteúdo (4xx que não é 401). Repetir o mesmo envio
// não adianta; o próximo ciclo coleta de novo.
type RejectedError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("o servidor recusou o envio: %d %s: %s (request_id %s)", e.Status, e.Code, e.Message, e.RequestID)
}

// TransientError: rede fora, servidor fora ou sobrecarregado (5xx, 408, 429). Vale tentar
// de novo, com espera crescente.
type TransientError struct {
	Err       error
	RequestID string
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("%v (request_id %s)", e.Err, e.RequestID)
}

func (e *TransientError) Unwrap() error { return e.Err }

// Client envia os relatórios com a credencial da máquina, sempre comprimidos em gzip.
type Client struct {
	BaseURL      string
	Credential   string
	AgentVersion string
	HTTP         *http.Client
	// Capabilities vão em cada check-in (Aula 8.1): o servidor só entrega jobs a quem
	// anuncia que sabe recebê-los.
	Capabilities []string
}

// NewClient monta o cliente com o prazo padrão.
func NewClient(baseURL, credential, agentVersion string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Credential:   credential,
		AgentVersion: agentVersion,
		HTTP:         &http.Client{Timeout: httpTimeout},
		Capabilities: []string{protocol.CapabilityJobs},
	}
}

// post envia v como JSON comprimido para path e decodifica a resposta 2xx em out.
func (c *Client) post(ctx context.Context, path string, v, out any) (string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &buf)
	if err != nil {
		return "", err
	}
	requestID := newRequestID()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "patchd-agent/"+c.AgentVersion)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return requestID, ctx.Err() // encerramento do agente: não é falha do servidor
		}
		if ClosedWithoutResponse(err) {
			err = fmt.Errorf("%w: a conexão abriu, mas foi fechada sem resposta", err)
		}
		return requestID, &TransientError{Err: err, RequestID: requestID}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return requestID, &TransientError{Err: err, RequestID: requestID}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if err := json.Unmarshal(data, out); err != nil {
			return requestID, &TransientError{Err: fmt.Errorf("resposta ilegível: %w", err), RequestID: requestID}
		}
		return requestID, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return requestID, ErrUnauthorized
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return requestID, &TransientError{Err: fmt.Errorf("o servidor respondeu %d", resp.StatusCode), RequestID: requestID}
	default:
		var e protocol.ErrorResponse
		_ = json.Unmarshal(data, &e)
		return requestID, &RejectedError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RequestID: requestID}
	}
}

// CheckIn faz o contato periódico e diz se o servidor já tem o inventário com esse hash.
func (c *Client) CheckIn(ctx context.Context, inventoryHash string) (protocol.CheckinResponse, error) {
	var out protocol.CheckinResponse
	_, err := c.post(ctx, "/api/v1/agent/checkin", protocol.CheckinRequest{
		AgentVersion: c.AgentVersion, InventoryHash: inventoryHash, SentAt: time.Now().UTC(), Capabilities: c.Capabilities,
	}, &out)
	return out, err
}

// SendJobResult envia o resultado (ou a recusa) de um job.
func (c *Client) SendJobResult(ctx context.Context, id int64, res protocol.JobResult) (protocol.SubmitResponse, error) {
	var out protocol.SubmitResponse
	_, err := c.post(ctx, fmt.Sprintf("%s%d/result", protocol.JobResultPath, id), res, &out)
	return out, err
}

// SendInventory envia o inventário.
func (c *Client) SendInventory(ctx context.Context, rep protocol.InventoryReport) (protocol.SubmitResponse, error) {
	var out protocol.SubmitResponse
	_, err := c.post(ctx, "/api/v1/agent/inventory", rep, &out)
	return out, err
}

// SendScan envia a busca de atualizações.
func (c *Client) SendScan(ctx context.Context, rep protocol.PatchScanReport) (protocol.SubmitResponse, error) {
	var out protocol.SubmitResponse
	_, err := c.post(ctx, "/api/v1/agent/scan", rep, &out)
	return out, err
}

// newRequestID gera o X-Request-ID: o mesmo valor aparece no log do servidor.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
