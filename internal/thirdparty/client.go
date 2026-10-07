package thirdparty

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/version"
)

// Client busca a versão de referência de cada fonte. Os endereços são campos para os testes
// (e para um espelho interno); o padrão são as fontes públicas.
type Client struct {
	HTTP        *http.Client
	UserAgent   string
	ChromeURL   string // https://versionhistory.googleapis.com
	FirefoxURL  string // o JSON de versões do Firefox
	GitHubURL   string // https://api.github.com
	HomebrewURL string // https://formulae.brew.sh
}

// NewClient devolve um cliente para as fontes públicas.
func NewClient(userAgent string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		UserAgent:   userAgent,
		ChromeURL:   "https://versionhistory.googleapis.com",
		FirefoxURL:  "https://product-details.mozilla.org/1.0/firefox_versions.json",
		GitHubURL:   "https://api.github.com",
		HomebrewURL: "https://formulae.brew.sh",
	}
}

// maxBody limita o tamanho das respostas (a maior, a lista de versões do Firefox no
// winget, tem poucas centenas de KB).
const maxBody = 8 << 20

// Latest devolve a versão de referência de uma fonte, já normalizada.
func (c *Client) Latest(ctx context.Context, s Source) (string, error) {
	var versions []string
	switch s.Type {
	case SourceChrome:
		// Versões do canal estável da plataforma, da mais nova para a mais antiga.
		var v struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		}
		u := fmt.Sprintf("%s/v1/chrome/platforms/%s/channels/stable/versions", c.ChromeURL, url.PathEscape(s.ID))
		if err := c.getJSON(ctx, u, &v); err != nil {
			return "", err
		}
		for _, x := range v.Versions {
			versions = append(versions, x.Version)
		}
	case SourceFirefox:
		var v map[string]string
		if err := c.getJSON(ctx, c.FirefoxURL, &v); err != nil {
			return "", err
		}
		if v[s.ID] == "" {
			return "", fmt.Errorf("firefox: a chave %s não está no JSON de versões", s.ID)
		}
		versions = []string{v[s.ID]}
	case SourceWinget:
		// O caminho do pacote é o identificador com os pontos virando pastas, sob a inicial
		// do fabricante: 7zip.7zip → manifests/7/7zip/7zip. Cada pasta numerada é uma versão;
		// as outras são pacotes vizinhos (Beta, ESR, os idiomas do Firefox).
		parts := strings.Split(s.ID, ".")
		if parts[0] == "" {
			return "", fmt.Errorf("winget: identificador %q inválido", s.ID)
		}
		for i, p := range parts {
			parts[i] = url.PathEscape(p)
		}
		u := fmt.Sprintf("%s/repos/microsoft/winget-pkgs/contents/manifests/%s/%s",
			c.GitHubURL, strings.ToLower(parts[0][:1]), strings.Join(parts, "/"))
		var dirs []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := c.getJSON(ctx, u, &dirs); err != nil {
			return "", err
		}
		for _, d := range dirs {
			if d.Type == "dir" && d.Name != "" && d.Name[0] >= '0' && d.Name[0] <= '9' {
				versions = append(versions, d.Name)
			}
		}
	case SourceHomebrew:
		var v struct {
			Version string `json:"version"`
		}
		if err := c.getJSON(ctx, fmt.Sprintf("%s/api/cask/%s.json", c.HomebrewURL, url.PathEscape(s.ID)), &v); err != nil {
			return "", err
		}
		// Alguns casks guardam "versão,build": vale a versão.
		ver, _, _ := strings.Cut(v.Version, ",")
		versions = []string{ver}
	default:
		return "", fmt.Errorf("fonte %q desconhecida", s.Type)
	}
	return newest(s, versions)
}

// newest devolve a maior versão comparável da lista.
func newest(s Source, versions []string) (string, error) {
	best := ""
	for _, raw := range versions {
		v, ok := Normalize(raw)
		if !ok {
			continue
		}
		if best == "" {
			best = v
		} else if c, err := version.CompareDotted(v, best); err == nil && c > 0 {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s: nenhuma versão comparável na resposta (%d itens)", s, len(versions))
	}
	return best, nil
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.UserAgent) // o GitHub recusa pedidos sem User-Agent
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("%s: HTTP %d", u, resp.StatusCode)
		if resp.StatusCode == http.StatusForbidden && strings.Contains(u, "api.github.com") {
			msg += " (a API do GitHub aceita 60 pedidos por hora sem autenticação)"
		}
		return fmt.Errorf("%s", msg)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return fmt.Errorf("%s: resposta ilegível: %w", u, err)
	}
	return nil
}
