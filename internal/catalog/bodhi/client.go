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
//
// O Bodhi é lento (perto de 20 s por página de 100 updates, na Aula 6.2) e derruba
// conexões no meio da resposta ("connection reset by peer"). Erro de rede, 429 e 5xx são
// repetidos até Attempts vezes, com espera crescente; 4xx e resposta grande demais, não:
// repetir não muda o resultado.
type Client struct {
	BaseURL     string
	HTTP        *http.Client
	UserAgent   string
	MaxPageSize int64
	Attempts    int           // tentativas por pedido (mínimo 1)
	RetryWait   time.Duration // espera antes da 2ª tentativa; a da n-ésima é n-1 vezes maior
}

// NewClient monta o cliente com os padrões.
func NewClient(userAgent string) *Client {
	return &Client{
		BaseURL:     DefaultBaseURL,
		HTTP:        &http.Client{Timeout: 2 * time.Minute},
		UserAgent:   userAgent,
		MaxPageSize: 64 << 20,
		Attempts:    4,
		RetryWait:   5 * time.Second,
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

// get faz o pedido com as repetições descritas em Client.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	attempts := max(c.Attempts, 1)
	var lastErr error
	for n := 1; n <= attempts; n++ {
		data, retry, err := c.getOnce(ctx, path, q)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !retry || n == attempts || ctx.Err() != nil {
			if n > 1 {
				return nil, fmt.Errorf("%w (%d tentativas)", err, n)
			}
			return nil, err
		}
		select {
		case <-time.After(c.RetryWait * time.Duration(n)):
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (interrompido entre tentativas: %v)", lastErr, ctx.Err())
		}
	}
	return nil, lastErr
}

// getOnce faz um pedido e diz se vale repetir. Pede JSON explicitamente: sem o Accept, o
// Bodhi responde HTML.
func (c *Client) getOnce(ctx context.Context, path string, q url.Values) (data []byte, retry bool, err error) {
	u := strings.TrimRight(c.BaseURL, "/") + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("Bodhi %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("Bodhi %s: %s: %s", path, resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, c.MaxPageSize+1))
	if err != nil {
		// A conexão caiu no meio do corpo: é o "connection reset by peer" do laboratório.
		return nil, true, fmt.Errorf("Bodhi %s: %w", path, err)
	}
	if int64(len(data)) > c.MaxPageSize {
		return nil, false, fmt.Errorf("Bodhi %s: resposta maior que o limite de %d bytes", path, c.MaxPageSize)
	}
	return data, false, nil
}
