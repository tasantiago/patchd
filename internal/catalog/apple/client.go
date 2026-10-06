package apple

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// Endereços padrão.
const (
	DefaultGDMFURL = "https://gdmf.apple.com/v2/pmv"
	DefaultSOFAURL = "https://sofafeed.macadmins.io/v2/macos_data_feed.json"
)

// Client baixa as duas fontes. GDMF usa um cliente HTTP que confia só na raiz da Apple;
// SOFA usa as raízes do sistema.
type Client struct {
	GDMFURL   string
	SOFAURL   string
	GDMF      *http.Client
	SOFA      *http.Client
	UserAgent string
	MaxSize   int64
}

// NewClient monta o cliente. O transporte do gdmf é uma cópia do padrão (com o proxy do
// ambiente, HTTPS_PROXY, preservado) em que a única raiz aceita é a da Apple.
func NewClient(userAgent string) (*Client, error) {
	pool, err := AppleRootPool()
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Client{
		GDMFURL:   DefaultGDMFURL,
		SOFAURL:   DefaultSOFAURL,
		GDMF:      &http.Client{Timeout: time.Minute, Transport: t},
		SOFA:      &http.Client{Timeout: time.Minute},
		UserAgent: userAgent,
		MaxSize:   32 << 20,
	}, nil
}

// FetchGDMF baixa e lê o gdmf. O hash é o SHA-256 do corpo: o gdmf não tem campo de versão.
func (c *Client) FetchGDMF(ctx context.Context) ([]GDMFVersion, string, error) {
	data, err := c.get(ctx, c.GDMF, c.GDMFURL)
	if err != nil {
		return nil, "", err
	}
	vs, err := ParseGDMF(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return vs, hex.EncodeToString(sum[:]), nil
}

// FetchSOFA baixa e lê o feed do SOFA.
func (c *Client) FetchSOFA(ctx context.Context) (SOFAFeed, error) {
	data, err := c.get(ctx, c.SOFA, c.SOFAURL)
	if err != nil {
		return SOFAFeed{}, err
	}
	return ParseSOFA(bytes.NewReader(data))
}

// get exige HTTPS, a não ser num endereço local (testes): sem TLS, a raiz fixada do gdmf
// não protegeria nada.
func (c *Client) get(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	u, err := neturl.Parse(url)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("endereço inválido: %q", url)
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, fmt.Errorf("%s: as fontes da Apple exigem https (http só em localhost)", url)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if int64(len(data)) > c.MaxSize {
		return nil, fmt.Errorf("%s: resposta maior que o limite de %d bytes", url, c.MaxSize)
	}
	return data, nil
}
