package winget

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/version"
)

// Client lê o winget-pkgs: a lista de versões de um pacote pela API do GitHub e o
// manifesto de instalador pelo raw.githubusercontent.com. Os endereços são campos para os
// testes (e para um espelho interno).
type Client struct {
	HTTP      *http.Client
	UserAgent string
	APIURL    string // https://api.github.com
	RawURL    string // https://raw.githubusercontent.com/microsoft/winget-pkgs/master
}

// NewClient devolve um cliente para o winget-pkgs público.
func NewClient(userAgent string) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		UserAgent: userAgent,
		APIURL:    "https://api.github.com",
		RawURL:    "https://raw.githubusercontent.com/microsoft/winget-pkgs/master",
	}
}

const maxManifest = 1 << 20 // o maior da amostra tem poucas dezenas de KB

var (
	idPart      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+&-]*$`)
	versionName = regexp.MustCompile(`^[0-9][A-Za-z0-9_+. -]*$`)
	dottedStart = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*`)
)

// ValidID diz se o identificador tem a forma dos do winget-pkgs (Google.Chrome,
// Notepad++.Notepad++, Mozilla.Firefox.pt-BR).
func ValidID(id string) bool {
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !idPart.MatchString(p) {
			return false
		}
	}
	return true
}

// PackagePath é a pasta do pacote no repositório: os pontos viram pastas, sob a inicial
// do primeiro trecho em minúscula (7zip.7zip → manifests/7/7zip/7zip).
func PackagePath(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("identificador %q fora da forma Fabricante.Programa", id)
	}
	parts := strings.Split(id, ".")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "manifests/" + strings.ToLower(id[:1]) + "/" + strings.Join(parts, "/"), nil
}

// Versions devolve as versões publicadas do pacote, da mais nova para a mais antiga. Só
// as pastas que começam por número: as outras são pacotes vizinhos (pt-BR, ESR, Beta).
func (c *Client) Versions(ctx context.Context, id string) ([]string, error) {
	path, err := PackagePath(id)
	if err != nil {
		return nil, err
	}
	var dirs []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	data, err := c.get(ctx, c.APIURL+"/repos/microsoft/winget-pkgs/contents/"+path, "application/json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &dirs); err != nil {
		return nil, fmt.Errorf("lista de versões de %s ilegível: %w", id, err)
	}
	var out []string
	for _, d := range dirs {
		if d.Type == "dir" && versionName.MatchString(d.Name) {
			out = append(out, d.Name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: nenhuma versão publicada no winget-pkgs", id)
	}
	slices.SortStableFunc(out, func(a, b string) int { return -compareVersion(a, b) })
	return out, nil
}

// compareVersion compara a parte numérica com pontos; o que não se compara fica para o
// fim, em ordem de texto.
func compareVersion(a, b string) int {
	na, nb := dottedStart.FindString(a), dottedStart.FindString(b)
	if c, err := version.CompareDotted(na, nb); err == nil && c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// Installer baixa e lê o manifesto de instalador de uma versão, e confere que ele é mesmo
// do pacote e da versão pedidos.
func (c *Client) Installer(ctx context.Context, id, ver string) (Manifest, error) {
	path, err := PackagePath(id)
	if err != nil {
		return Manifest{}, err
	}
	if !versionName.MatchString(ver) || strings.Contains(ver, "..") {
		return Manifest{}, fmt.Errorf("versão %q inválida", ver)
	}
	u := fmt.Sprintf("%s/%s/%s/%s.installer.yaml", c.RawURL, path, url.PathEscape(ver), url.PathEscape(id))
	data, err := c.get(ctx, u, "")
	if err != nil {
		return Manifest{}, err
	}
	m, err := ParseInstaller(data)
	if err != nil {
		return m, fmt.Errorf("%s %s: %w", id, ver, err)
	}
	if !strings.EqualFold(m.PackageIdentifier, id) || m.PackageVersion != ver {
		return m, fmt.Errorf("o manifesto em %s/%s é de %s %s", id, ver, m.PackageIdentifier, m.PackageVersion)
	}
	return m, nil
}

func (c *Client) get(ctx context.Context, u, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent) // o GitHub recusa pedidos sem User-Agent
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: não existe no winget-pkgs (HTTP 404)", u)
	case resp.StatusCode == http.StatusForbidden && strings.HasPrefix(u, c.APIURL):
		return nil, fmt.Errorf("%s: HTTP 403 (a API do GitHub aceita 60 pedidos por hora sem autenticação; use -version para pular a lista)", u)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifest {
		return nil, fmt.Errorf("%s: resposta maior que %d bytes", u, maxManifest)
	}
	return data, nil
}
