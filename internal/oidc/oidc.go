// Package oidc é o login do painel pelo RHBK (Red Hat build of Keycloak), ou qualquer
// provedor OpenID Connect (Aula 9.4): fluxo de código de autorização com PKCE, cliente
// confidencial, e o ID token conferido aqui mesmo (assinatura RS256 pela JWKS do
// provedor, emissor, audiência, prazo e nonce). O perfil do painel vem de um claim do ID
// token (grupos ou papéis), como os grupos do AD na Aula 7.3.
//
// Só a biblioteca padrão: o que o patchd confere fica à vista neste arquivo.
package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
)

// Limites.
const (
	clockSkew     = 2 * time.Minute  // tolerância entre o relógio do servidor e o do provedor
	jwksMinReload = time.Minute      // uma chave desconhecida recarrega a JWKS, no máximo a cada minuto
	maxBody       = 1 << 20          // respostas do provedor
	httpTimeout   = 15 * time.Second // descoberta, JWKS e troca do código
)

// Config é a configuração do login pelo provedor.
type Config struct {
	Issuer       string // ex.: https://sso.exemplo/realms/patchd (o "iss" dos tokens, exato)
	ClientID     string
	ClientSecret string
	RedirectURL  string // ex.: https://patchd.exemplo:8443/painel/oidc/retorno
	// RolesClaim é o caminho do claim com os grupos ou papéis, com pontos para descer:
	// "groups" (mapeador de grupos) ou "realm_access.roles" (papéis do realm).
	RolesClaim string
	AdminRole  string // valor no claim que dá o perfil admin
	ReadRole   string // valor que dá o perfil leitura; vazio: só admins entram
}

// Validate confere a configuração, sem rede.
func (c Config) Validate() error {
	var problems []error
	if err := checkURL("PATCHD_OIDC_ISSUER", c.Issuer); err != nil {
		problems = append(problems, err)
	}
	if err := checkURL("PATCHD_OIDC_REDIRECT_URL", c.RedirectURL); err != nil {
		problems = append(problems, err)
	} else if u, _ := url.Parse(c.RedirectURL); u.Path != "/painel/oidc/retorno" {
		problems = append(problems, errors.New("PATCHD_OIDC_REDIRECT_URL: o caminho tem de ser /painel/oidc/retorno"))
	}
	if c.ClientID == "" {
		problems = append(problems, errors.New("PATCHD_OIDC_CLIENT_ID vazio"))
	}
	if c.ClientSecret == "" {
		problems = append(problems, errors.New("PATCHD_OIDC_CLIENT_SECRET_FILE (ou PATCHD_OIDC_CLIENT_SECRET) vazio"))
	}
	if c.RolesClaim == "" || strings.HasPrefix(c.RolesClaim, ".") || strings.HasSuffix(c.RolesClaim, ".") || strings.Contains(c.RolesClaim, "..") {
		problems = append(problems, fmt.Errorf("PATCHD_OIDC_ROLES_CLAIM inválido (%q)", c.RolesClaim))
	}
	if c.AdminRole == "" {
		problems = append(problems, errors.New("PATCHD_OIDC_ADMIN_ROLE vazio"))
	}
	if c.AdminRole != "" && c.AdminRole == c.ReadRole {
		problems = append(problems, errors.New("PATCHD_OIDC_ADMIN_ROLE e PATCHD_OIDC_READ_ROLE iguais"))
	}
	return errors.Join(problems...)
}

// checkURL: https, ou http só para o próprio host (laboratório), sem query nem fragmento.
func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return fmt.Errorf("%s vazio", name)
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("%s: URL inválida (%q)", name, raw)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%s: use https (http só para 127.0.0.1 ou localhost, no laboratório)", name)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Identity é quem entrou: o usuário (preferred_username) e o perfil do painel.
type Identity struct {
	Subject  string
	Username string
	Name     string // nome de exibição (claim "name"), já limpo (Aula 15.5); pode ser vazio
	Role     string
	Roles    []string // o que veio no claim, para o log quando nenhum serve
}

// Provider fala com o provedor. A descoberta e a JWKS são buscadas na primeira vez que
// fazem falta e guardadas: o servidor sobe mesmo com o provedor fora do ar.
type Provider struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu        sync.Mutex
	authURL   string
	tokenURL  string
	jwksURL   string
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
	discovery bool
}

// New prepara o provedor; client nil usa um cliente com o prazo padrão (CAs do sistema).
func New(cfg Config, client *http.Client) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &Provider{cfg: cfg, http: client, now: time.Now}, nil
}

// Issuer devolve o emissor configurado (para o log).
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// Public devolve a configuração sem o segredo do cliente (Aula 15.3: a tela de acesso).
func (p *Provider) Public() Config {
	c := p.cfg
	c.ClientSecret = ""
	return c
}

// Probe busca a descoberta de novo e confere, sem guardar nada (Aula 15.3: o botão de
// teste). O Discover guarda o resultado na primeira vez e não serve para testar depois.
func (p *Provider) Probe(ctx context.Context) error {
	_, err := p.fetchDiscovery(ctx)
	return err
}

// discovery do provedor: .well-known/openid-configuration.
type discoveryDoc struct {
	Issuer        string `json:"issuer"`
	AuthEndpoint  string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

// Discover busca (uma vez) os endereços do provedor. O "issuer" do documento tem de ser
// exatamente o configurado: é ele que os tokens trazem.
func (p *Provider) Discover(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discovery {
		return nil
	}
	doc, err := p.fetchDiscovery(ctx)
	if err != nil {
		return err
	}
	p.authURL, p.tokenURL, p.jwksURL = doc.AuthEndpoint, doc.TokenEndpoint, doc.JWKSURI
	p.discovery = true
	return nil
}

// fetchDiscovery busca e confere o documento de descoberta.
func (p *Provider) fetchDiscovery(ctx context.Context) (discoveryDoc, error) {
	var doc discoveryDoc
	if err := p.getJSON(ctx, strings.TrimRight(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
		return doc, fmt.Errorf("descoberta do provedor: %w", err)
	}
	if doc.Issuer != p.cfg.Issuer {
		return doc, fmt.Errorf("o provedor se apresenta como %q, não como o PATCHD_OIDC_ISSUER configurado (%q)", doc.Issuer, p.cfg.Issuer)
	}
	for name, u := range map[string]string{"authorization_endpoint": doc.AuthEndpoint, "token_endpoint": doc.TokenEndpoint, "jwks_uri": doc.JWKSURI} {
		if err := checkURL(name, u); err != nil {
			return doc, fmt.Errorf("descoberta do provedor: %w", err)
		}
	}
	return doc, nil
}

func (p *Provider) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s respondeu %d", u, resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// Login é o que o painel guarda entre a ida ao provedor e a volta: o state (que também
// vai no cookie do navegador), o nonce (que volta dentro do ID token) e o verificador do
// PKCE (que só o servidor conhece).
type Login struct {
	State, Nonce, Verifier string
}

// NewLogin sorteia os três valores (32 bytes cada).
func NewLogin() Login {
	return Login{State: random(), Nonce: random(), Verifier: random()}
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthURL monta o endereço para onde o navegador vai (precisa da descoberta feita).
func (p *Provider) AuthURL(l Login) string {
	sum := sha256.Sum256([]byte(l.Verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURL},
		"scope":                 {"openid profile"},
		"state":                 {l.State},
		"nonce":                 {l.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	sep := "?"
	if strings.Contains(p.authURL, "?") {
		sep = "&"
	}
	return p.authURL + sep + q.Encode()
}

// Exchange troca o código pelo ID token, confere o token e devolve quem entrou.
func (p *Provider) Exchange(ctx context.Context, code string, l Login) (Identity, error) {
	if err := p.Discover(ctx); err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {l.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// client_secret_basic (RFC 6749, 2.3.1): id e segredo codificados como formulário.
	req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	resp, err := p.http.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("troca do código: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Identity{}, fmt.Errorf("troca do código: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &e)
		return Identity{}, fmt.Errorf("troca do código: o provedor respondeu %d %s %s", resp.StatusCode, e.Error, trim(e.Desc))
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.IDToken == "" {
		return Identity{}, errors.New("troca do código: resposta sem id_token")
	}
	claims, err := p.VerifyIDToken(ctx, tok.IDToken, l.Nonce)
	if err != nil {
		return Identity{}, err
	}
	return p.identity(claims)
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// VerifyIDToken confere o ID token: assinatura RS256 com uma chave da JWKS do provedor,
// emissor exato, audiência com o client_id (e azp, se houver mais de uma), prazo, emissão
// e nonce. Devolve os claims.
func (p *Provider) VerifyIDToken(ctx context.Context, raw, nonce string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("ID token fora do formato")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return nil, fmt.Errorf("cabeçalho do ID token: %w", err)
	}
	if head.Alg != "RS256" {
		return nil, fmt.Errorf("ID token assinado com %q; o patchd aceita só RS256", head.Alg)
	}
	key, err := p.key(ctx, head.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("assinatura do ID token ilegível")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("assinatura do ID token não confere")
	}
	var claims map[string]any
	if err := decodePart(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("conteúdo do ID token: %w", err)
	}
	now := p.now()
	str := func(k string) string { s, _ := claims[k].(string); return s }
	num := func(k string) (time.Time, bool) {
		f, ok := claims[k].(float64)
		return time.Unix(int64(f), 0), ok
	}
	var aud []string
	switch v := claims["aud"].(type) {
	case string:
		aud = []string{v}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				aud = append(aud, s)
			}
		}
	}
	exp, okExp := num("exp")
	iat, okIat := num("iat")
	switch {
	case str("iss") != p.cfg.Issuer:
		return nil, fmt.Errorf("ID token de outro emissor (%q)", str("iss"))
	case !slices.Contains(aud, p.cfg.ClientID):
		return nil, errors.New("ID token para outro cliente (aud)")
	case len(aud) > 1 && str("azp") != p.cfg.ClientID:
		return nil, errors.New("ID token com várias audiências e azp de outro cliente")
	case !okExp || now.After(exp.Add(clockSkew)):
		return nil, errors.New("ID token vencido")
	case !okIat || iat.After(now.Add(clockSkew)):
		return nil, errors.New("ID token emitido no futuro (relógio do servidor ou do provedor errado?)")
	case nonce == "" || str("nonce") != nonce:
		return nil, errors.New("nonce do ID token não confere (resposta repetida ou de outro login)")
	case str("sub") == "":
		return nil, errors.New("ID token sem sub")
	}
	return claims, nil
}

func decodePart(s string, out any) error {
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// key devolve a chave pública do kid, recarregando a JWKS quando o provedor troca de
// chave (no máximo uma vez por minuto: um kid inventado não vira um ataque ao provedor).
func (p *Provider) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if err := p.Discover(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	k, ok := p.keys[kid]
	stale := p.now().Sub(p.keysAt) >= jwksMinReload
	jwksURL := p.jwksURL
	p.mu.Unlock()
	if ok {
		return k, nil
	}
	if !stale && p.keys != nil {
		return nil, fmt.Errorf("chave %q desconhecida na JWKS do provedor", kid)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, jwksURL, &set); err != nil {
		return nil, fmt.Errorf("JWKS do provedor: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range set.Keys {
		if j.Kty != "RSA" || (j.Use != "" && j.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(j.N)
		e, err2 := base64.RawURLEncoding.DecodeString(j.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 {
			continue // chave fraca demais
		}
		keys[j.Kid] = pub
	}
	p.mu.Lock()
	p.keys, p.keysAt = keys, p.now()
	p.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("chave %q desconhecida na JWKS do provedor", kid)
}

// ErrNoUsername: o ID token não traz um preferred_username que sirva de nome no painel.
var ErrNoUsername = errors.New("o ID token não traz um preferred_username válido")

// identity tira dos claims o usuário e o perfil.
func (p *Provider) identity(claims map[string]any) (Identity, error) {
	sub, _ := claims["sub"].(string)
	user, _ := claims["preferred_username"].(string)
	user = panel.NormalizeUsername(user)
	id := Identity{Subject: sub, Username: user, Name: displayName(claims), Roles: RolesAt(claims, p.cfg.RolesClaim)}
	if !panel.ValidUsername(user) {
		return id, ErrNoUsername
	}
	switch {
	case slices.Contains(id.Roles, p.cfg.AdminRole):
		id.Role = panel.RoleAdmin
	case p.cfg.ReadRole != "" && slices.Contains(id.Roles, p.cfg.ReadRole):
		id.Role = panel.RoleRead
	default:
		return id, panel.ErrNoRole
	}
	return id, nil
}

// displayName: o claim "name" (no RHBK, nome e sobrenome do usuário, que a federação com o
// AD preenche); sem ele, given_name e family_name.
func displayName(claims map[string]any) string {
	str := func(k string) string { s, _ := claims[k].(string); return s }
	if n := panel.CleanDisplayName(str("name")); n != "" {
		return n
	}
	return panel.CleanDisplayName(str("given_name") + " " + str("family_name"))
}

// RolesAt lê o claim no caminho (com pontos): uma lista de textos ou um texto. Grupos com
// o caminho completo ("/patchd-admin") valem como o nome simples.
func RolesAt(claims map[string]any, path string) []string {
	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	var out []string
	add := func(s string) {
		if s = strings.TrimPrefix(strings.TrimSpace(s), "/"); s != "" {
			out = append(out, s)
		}
	}
	switch v := cur.(type) {
	case string:
		add(v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				add(s)
			}
		}
	}
	return out
}
