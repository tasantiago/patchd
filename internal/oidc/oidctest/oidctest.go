// Package oidctest é um provedor OpenID Connect de mentira para os testes (Aula 9.4):
// descoberta, JWKS e o token endpoint, que confere o segredo do cliente, o código e o
// PKCE, e devolve um ID token assinado com os claims que o teste escolher.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Valores fixos do provedor de teste.
const (
	ClientID     = "patchd-painel"
	ClientSecret = "segredo+com/simbolos"
	Code         = "codigo-bom"
)

// Provider é o provedor de mentira.
type Provider struct {
	*httptest.Server
	t   testing.TB
	mu  sync.Mutex
	key *rsa.PrivateKey
	kid string
	// Claims vão no próximo ID token; Nonce é o do último pedido de autorização.
	claims    map[string]any
	challenge string
	nonce     string
	alg       string
	jwksHits  int
}

// New sobe o provedor; o emissor é Issuer().
func New(t testing.TB) *Provider {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{t: t, key: k, kid: "k1", alg: "RS256"}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	mux.HandleFunc("GET /realms/patchd/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := p.Issuer()
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss,
			"authorization_endpoint": iss + "/protocol/openid-connect/auth",
			"token_endpoint":         iss + "/protocol/openid-connect/token",
			"jwks_uri":               iss + "/protocol/openid-connect/certs"})
	})
	mux.HandleFunc("GET /realms/patchd/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.jwksHits++
		pub := p.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": p.kid, "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}}})
	})
	mux.HandleFunc("POST /realms/patchd/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		// RFC 6749, 2.3.1: id e segredo vêm codificados como formulário dentro do Basic.
		id, secret, ok := r.BasicAuth()
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
		if !ok || id != ClientID || secret != ClientSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		p.mu.Lock()
		okPKCE := base64.RawURLEncoding.EncodeToString(sum[:]) == p.challenge
		p.mu.Unlock()
		if r.PostFormValue("code") != Code || !okPKCE || r.PostFormValue("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Code not valid"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": p.Sign(nil), "token_type": "Bearer"})
	})
	return p
}

// Issuer é o emissor ("iss") do provedor.
func (p *Provider) Issuer() string { return p.URL + "/realms/patchd" }

// Authorize faz o papel do navegador no provedor: guarda o desafio do PKCE e o nonce do
// endereço de autorização, e põe nos claims do próximo token os do usuário pedido.
func (p *Provider) Authorize(authURL string, user string, groups ...string) {
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	agora := time.Now().Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenge, p.nonce = q.Get("code_challenge"), q.Get("nonce")
	gs := make([]any, len(groups))
	for i, g := range groups {
		gs[i] = g
	}
	p.claims = map[string]any{"iss": p.Issuer(), "aud": ClientID, "sub": "sub-" + user, "nonce": p.nonce,
		"iat": agora, "exp": agora + 300, "preferred_username": user, "groups": gs}
}

// SetClaims troca os claims do próximo token.
func (p *Provider) SetClaims(c map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = c
}

// Claims devolve uma cópia dos claims atuais.
func (p *Provider) Claims() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := map[string]any{}
	for k, v := range p.claims {
		c[k] = v
	}
	return c
}

// SetChallenge define o desafio do PKCE que o token endpoint vai exigir.
func (p *Provider) SetChallenge(c string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenge = c
}

// RotateKey troca a chave de assinatura (kid novo).
func (p *Provider) RotateKey(kid string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key, p.kid = k, kid
}

// SetAlg troca o "alg" do cabeçalho (para testar recusas).
func (p *Provider) SetAlg(alg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alg = alg
}

// JWKSHits diz quantas vezes a JWKS foi buscada.
func (p *Provider) JWKSHits() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksHits
}

// Sign assina os claims atuais (com os ajustes) com a chave atual.
func (p *Provider) Sign(ajuste func(map[string]any)) string {
	c := p.Claims()
	if ajuste != nil {
		ajuste(c)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h, _ := json.Marshal(map[string]string{"alg": p.alg, "kid": p.kid, "typ": "JWT"})
	b, _ := json.Marshal(c)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}
