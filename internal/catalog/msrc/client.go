package msrc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL é a API CVRF do MSRC. A versão 3.0 não exige chave.
const DefaultBaseURL = "https://api.msrc.microsoft.com/cvrf/v3.0"

// idPattern: os documentos são identificados por ano e mês em inglês (2026-Sep).
var idPattern = regexp.MustCompile(`^[0-9]{4}-[A-Z][a-z]{2}$`)

// Client baixa a lista e os documentos. Cada resposta tem um teto de tamanho: o documento
// de setembro de 2026 tem 20 MB, e um servidor que mandasse gigabytes não derruba o patchd.
type Client struct {
	BaseURL     string
	HTTP        *http.Client
	UserAgent   string
	MaxListSize int64
	MaxDocSize  int64
}

// NewClient monta o cliente com os padrões.
func NewClient(userAgent string) *Client {
	return &Client{
		BaseURL:     DefaultBaseURL,
		HTTP:        &http.Client{Timeout: 5 * time.Minute},
		UserAgent:   userAgent,
		MaxListSize: 16 << 20,
		MaxDocSize:  256 << 20,
	}
}

// Updates baixa a lista de documentos.
func (c *Client) Updates(ctx context.Context) ([]Update, error) {
	data, err := c.get(ctx, "/updates", c.MaxListSize)
	if err != nil {
		return nil, err
	}
	return ParseUpdates(bytes.NewReader(data))
}

// Document baixa e lê um documento.
func (c *Client) Document(ctx context.Context, id string) (Document, error) {
	if !idPattern.MatchString(id) {
		return Document{}, fmt.Errorf("ID de documento do MSRC inválido: %q", id)
	}
	data, err := c.get(ctx, "/cvrf/"+id, c.MaxDocSize)
	if err != nil {
		return Document{}, err
	}
	d, err := Parse(bytes.NewReader(data))
	if err != nil {
		return d, err
	}
	if d.ID != id {
		return d, fmt.Errorf("pedido o documento %s, recebido o %s", id, d.ID)
	}
	return d, nil
}

func (c *Client) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("MSRC %s: %s: %s", path, resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("MSRC %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("MSRC %s: resposta maior que o limite de %d bytes", path, limit)
	}
	return data, nil
}
