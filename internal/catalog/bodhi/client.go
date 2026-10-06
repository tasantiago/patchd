package bodhi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL é a instância pública do Bodhi. A API é aberta para leitura.
const DefaultBaseURL = "https://bodhi.fedoraproject.org"

// PageSize é o tamanho de página pedido. O Bodhi aceita até 1000, mas cada update vem com
// os comentários: páginas menores mantêm as respostas pequenas e os timeouts raros.
const PageSize = 100

// Client consulta a API do Bodhi.
type Client struct {
	BaseURL     string
	HTTP        *http.Client
	UserAgent   string
	MaxPageSize int64
}

// NewClient monta o cliente com os padrões.
func NewClient(userAgent string) *Client {
	return &Client{
		BaseURL:     DefaultBaseURL,
		HTTP:        &http.Client{Timeout: 2 * time.Minute},
		UserAgent:   userAgent,
		MaxPageSize: 64 << 20,
	}
}

// Releases devolve as versões do Fedora em suporte (state=current), sem EPEL, Flatpak e
// Container.
func (c *Client) Releases(ctx context.Context) ([]Release, error) {
	var out []Release
	for page := 1; ; page++ {
		q := url.Values{"state": {"current"}, "rows_per_page": {strconv.Itoa(PageSize)}, "page": {strconv.Itoa(page)}}
		data, err := c.get(ctx, "/releases/", q)
		if err != nil {
			return nil, err
		}
		p, err := ParseReleases(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		for _, r := range p.Releases {
			if r.IsFedora() {
				out = append(out, r)
			}
		}
		if page >= p.Pages {
			return out, nil
		}
	}
}

// Updates pede uma página dos updates de segurança estáveis de uma versão. since zero
// pede todos; senão, só os publicados (date_pushed) a partir de since.
func (c *Client) Updates(ctx context.Context, release string, since time.Time, page int) (UpdatesPage, error) {
	if !ValidRelease(release) {
		return UpdatesPage{}, fmt.Errorf("versão do Fedora inválida: %q", release)
	}
	q := url.Values{
		"type":          {"security"},
		"status":        {"stable"},
		"releases":      {release},
		"rows_per_page": {strconv.Itoa(PageSize)},
		"page":          {strconv.Itoa(page)},
	}
	if !since.IsZero() {
		q.Set("pushed_since", since.UTC().Format("2006-01-02T15:04:05"))
	}
	data, err := c.get(ctx, "/updates/", q)
	if err != nil {
		return UpdatesPage{}, err
	}
	return ParseUpdates(bytes.NewReader(data))
}

// get pede JSON explicitamente: sem o Accept, o Bodhi responde HTML.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("Bodhi %s: %s: %s", path, resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxPageSize+1))
	if err != nil {
		return nil, fmt.Errorf("Bodhi %s: %w", path, err)
	}
	if int64(len(data)) > c.MaxPageSize {
		return nil, fmt.Errorf("Bodhi %s: resposta maior que o limite de %d bytes", path, c.MaxPageSize)
	}
	return data, nil
}
