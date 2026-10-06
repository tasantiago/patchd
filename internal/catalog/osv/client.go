package osv

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

// DefaultBaseURL é o bucket público do OSV.dev, por HTTPS (https://google.github.io/osv.dev/data/).
// Os registros ficam em <base>/<ecossistema>/<ID>.json e a lista de mudanças em
// <base>/<ecossistema>/modified_id.csv.
const DefaultBaseURL = "https://storage.googleapis.com/osv-vulnerabilities"

// idPattern: o ID vira parte do caminho da URL; nada de barra nem de "..".
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// Client baixa a lista de mudanças e os registros de um ecossistema do bucket.
type Client struct {
	BaseURL       string
	Ecosystem     string // diretório do ecossistema no bucket, ex.: Ubuntu
	HTTP          *http.Client
	UserAgent     string
	MaxListSize   int64
	MaxRecordSize int64
}

// NewClient monta o cliente com os padrões. A lista do Ubuntu tem uns 3,4 MB; o maior
// registro (USN do kernel, com a lista de versões) fica bem abaixo do limite.
func NewClient(ecosystem, userAgent string) *Client {
	return &Client{
		BaseURL:       DefaultBaseURL,
		Ecosystem:     ecosystem,
		HTTP:          &http.Client{Timeout: 2 * time.Minute},
		UserAgent:     userAgent,
		MaxListSize:   64 << 20,
		MaxRecordSize: 64 << 20,
	}
}

// ModifiedIDs baixa o modified_id.csv do ecossistema e devolve as linhas com o prefixo.
func (c *Client) ModifiedIDs(ctx context.Context, prefix string) ([]Entry, error) {
	data, err := c.get(ctx, "/"+c.Ecosystem+"/modified_id.csv", c.MaxListSize)
	if err != nil {
		return nil, err
	}
	return ParseModifiedIDs(bytes.NewReader(data), prefix)
}

// Record baixa e lê um registro, e confere que veio o registro pedido.
func (c *Client) Record(ctx context.Context, id string) (Record, error) {
	if !idPattern.MatchString(id) {
		return Record{}, fmt.Errorf("ID de registro OSV inválido: %q", id)
	}
	data, err := c.get(ctx, "/"+c.Ecosystem+"/"+id+".json", c.MaxRecordSize)
	if err != nil {
		return Record{}, err
	}
	rec, err := Parse(bytes.NewReader(data))
	if err != nil {
		return rec, fmt.Errorf("%s: %w", id, err)
	}
	if rec.ID != id {
		return rec, fmt.Errorf("pedido o registro %s, recebido o %s", id, rec.ID)
	}
	return rec, nil
}

// get confere o status antes de ler o corpo: um registro que não existe volta como XML de
// erro do bucket, e um parser de JSON daria uma mensagem enganosa.
func (c *Client) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OSV %s: %s", path, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("OSV %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("OSV %s: resposta maior que o limite de %d bytes", path, limit)
	}
	return data, nil
}
