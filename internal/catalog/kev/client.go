package kev

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL é o feed JSON oficial da CISA. O espelho oficial no GitHub
// (https://raw.githubusercontent.com/cisagov/kev-data/develop/known_exploited_vulnerabilities.json)
// serve como alternativa quando o proxy bloqueia o cisa.gov: PATCHD_KEV_URL.
const DefaultURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

// Client baixa o feed.
type Client struct {
	URL       string
	HTTP      *http.Client
	UserAgent string
	MaxSize   int64
}

// NewClient monta o cliente com os padrões. O feed tem 1,8 MB.
func NewClient(userAgent string) *Client {
	return &Client{URL: DefaultURL, HTTP: &http.Client{Timeout: 2 * time.Minute}, UserAgent: userAgent, MaxSize: 64 << 20}
}

// Fetch baixa e lê o catálogo. Exige HTTPS, a não ser em localhost (testes).
func (c *Client) Fetch(ctx context.Context) (Catalog, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return Catalog{}, fmt.Errorf("endereço do KEV inválido: %q", c.URL)
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return Catalog{}, fmt.Errorf("%s: o KEV exige https (http só em localhost)", c.URL)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Catalog{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Catalog{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return Catalog{}, fmt.Errorf("KEV: %s: %s", resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxSize+1))
	if err != nil {
		return Catalog{}, fmt.Errorf("KEV: %w", err)
	}
	if int64(len(data)) > c.MaxSize {
		return Catalog{}, fmt.Errorf("KEV: resposta maior que o limite de %d bytes", c.MaxSize)
	}
	return Parse(bytes.NewReader(data))
}
