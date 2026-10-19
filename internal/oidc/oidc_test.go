package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/oidc/oidctest"
	"github.com/tasantiago/patchd/internal/panel"
)

func configDe(p *oidctest.Provider) Config {
	return Config{Issuer: p.Issuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "https://127.0.0.1:18443/painel/oidc/retorno", RolesClaim: "groups", AdminRole: "patchd-admin", ReadRole: "patchd-leitura"}
}

// login faz o caminho do navegador: AuthURL, o provedor guarda desafio e nonce, e volta.
func login(t *testing.T, p *oidctest.Provider, prov *Provider, user string, groups ...string) Login {
	t.Helper()
	if err := prov.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := NewLogin()
	auth := prov.AuthURL(l)
	u, _ := url.Parse(auth)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") != l.State || q.Get("nonce") != l.Nonce ||
		q.Get("redirect_uri") != "https://127.0.0.1:18443/painel/oidc/retorno" || !strings.Contains(q.Get("scope"), "openid") ||
		strings.Contains(auth, l.Verifier) {
		t.Fatalf("AuthURL: %s", auth)
	}
	p.Authorize(auth, user, groups...)
	return l
}

func TestLoginCompleto(t *testing.T) {
	p := oidctest.New(t)
	prov, err := New(configDe(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	l := login(t, p, prov, "Fulano.Lab", "outro", "patchd-admin")
	id, err := prov.Exchange(ctx, oidctest.Code, l)
	if err != nil || id.Username != "fulano.lab" || id.Role != panel.RoleAdmin || id.Subject != "sub-Fulano.Lab" || id.Name != "Nome de Fulano.Lab" {
		t.Fatalf("admin: %+v %v", id, err)
	}
	// Sem o claim "name": nome e sobrenome; sem nenhum dos três, vazio.
	for want, c := range map[string]map[string]any{
		"Ana Souza": {"given_name": "Ana", "family_name": " Souza"},
		"":          {},
		"Bia Lima":  {"name": "  Bia\tLima\u200f "},
	} {
		if got := displayName(c); got != want {
			t.Errorf("displayName(%v) = %q, quer %q", c, got, want)
		}
	}
	// Leitura pelo outro grupo; o grupo com o caminho completo também vale.
	l = login(t, p, prov, "beltrano", "/patchd-leitura")
	if id, err := prov.Exchange(ctx, oidctest.Code, l); err != nil || id.Role != panel.RoleRead {
		t.Errorf("leitura: %+v %v", id, err)
	}
	// Sem grupo do painel: ErrNoRole, e os grupos vêm para o log.
	l = login(t, p, prov, "ciclano", "financeiro")
	if id, err := prov.Exchange(ctx, oidctest.Code, l); !errors.Is(err, panel.ErrNoRole) || len(id.Roles) != 1 {
		t.Errorf("sem grupo: %+v %v", id, err)
	}
	// Nome que não serve no painel.
	l = login(t, p, prov, "nome com espaço", "patchd-admin")
	if _, err := prov.Exchange(ctx, oidctest.Code, l); !errors.Is(err, ErrNoUsername) {
		t.Errorf("nome inválido: %v", err)
	}
	// Código errado, ou o verificador do PKCE de outro login: o provedor recusa a troca.
	l = login(t, p, prov, "fulano", "patchd-admin")
	if _, err := prov.Exchange(ctx, "codigo-ruim", l); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("código ruim: %v", err)
	}
	outro := NewLogin()
	outro.Nonce = l.Nonce
	if _, err := prov.Exchange(ctx, oidctest.Code, outro); err == nil {
		t.Error("verificador do PKCE de outro login aceito")
	}
	// Segredo errado.
	cfg := configDe(p)
	cfg.ClientSecret = "errado"
	ruim, _ := New(cfg, nil)
	l = login(t, p, ruim, "fulano", "patchd-admin")
	if _, err := ruim.Exchange(ctx, oidctest.Code, l); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("segredo errado: %v", err)
	}
}

func TestIDTokenRecusado(t *testing.T) {
	p := oidctest.New(t)
	prov, _ := New(configDe(p), nil)
	ctx := context.Background()
	if err := prov.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	p.Authorize(prov.AuthURL(Login{Nonce: "n1"}), "fulano", "patchd-admin")
	if _, err := prov.VerifyIDToken(ctx, p.Sign(nil), "n1"); err != nil {
		t.Fatalf("token bom recusado: %v", err)
	}
	for nome, c := range map[string]struct {
		ajuste func(map[string]any)
		nonce  string
		msg    string
	}{
		"nonce de outro login": {nil, "n2", "nonce"},
		"vencido":              {func(m map[string]any) { m["exp"] = time.Now().Add(-10 * time.Minute).Unix() }, "n1", "vencido"},
		"emitido no futuro":    {func(m map[string]any) { m["iat"] = time.Now().Add(10 * time.Minute).Unix() }, "n1", "futuro"},
		"outro emissor":        {func(m map[string]any) { m["iss"] = "https://outro/realms/x" }, "n1", "emissor"},
		"outro cliente":        {func(m map[string]any) { m["aud"] = "outro-cliente" }, "n1", "aud"},
		"várias audiências": {func(m map[string]any) {
			m["aud"] = []string{oidctest.ClientID, "x"}
			m["azp"] = "x"
		}, "n1", "azp"},
		"sem sub": {func(m map[string]any) { delete(m, "sub") }, "n1", "sub"},
	} {
		if _, err := prov.VerifyIDToken(ctx, p.Sign(c.ajuste), c.nonce); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	// Conteúdo trocado com a assinatura de outro token.
	parts := strings.Split(p.Sign(nil), ".")
	c := p.Claims()
	c["preferred_username"], c["groups"] = "intruso", []string{"patchd-admin"}
	mexido, _ := json.Marshal(c)
	if _, err := prov.VerifyIDToken(ctx, parts[0]+"."+base64.RawURLEncoding.EncodeToString(mexido)+"."+parts[2], "n1"); err == nil ||
		!strings.Contains(err.Error(), "assinatura") {
		t.Errorf("adulterado: %v", err)
	}
	// "alg": "none" (ou outro): recusado antes de qualquer chave.
	p.SetAlg("none")
	if _, err := prov.VerifyIDToken(ctx, p.Sign(nil), "n1"); err == nil || !strings.Contains(err.Error(), "RS256") {
		t.Errorf("alg none: %v", err)
	}
}

func TestTrocaDeChave(t *testing.T) {
	p := oidctest.New(t)
	prov, _ := New(configDe(p), nil)
	agora := time.Now()
	prov.now = func() time.Time { return agora }
	ctx := context.Background()
	_ = prov.Discover(ctx)
	p.Authorize(prov.AuthURL(Login{Nonce: "n1"}), "fulano", "patchd-admin")
	if _, err := prov.VerifyIDToken(ctx, p.Sign(nil), "n1"); err != nil {
		t.Fatal(err)
	}
	// O provedor troca de chave: um kid novo recarrega a JWKS, mas só depois de 1 minuto.
	p.RotateKey("k2")
	if _, err := prov.VerifyIDToken(ctx, p.Sign(nil), "n1"); err == nil || !strings.Contains(err.Error(), "desconhecida") {
		t.Errorf("antes de 1 minuto: %v", err)
	}
	agora = agora.Add(2 * time.Minute)
	ajuste := func(m map[string]any) { m["iat"], m["exp"] = agora.Unix(), agora.Add(5*time.Minute).Unix() }
	if _, err := prov.VerifyIDToken(ctx, p.Sign(ajuste), "n1"); err != nil {
		t.Errorf("depois de 1 minuto: %v", err)
	}
	if p.JWKSHits() != 2 {
		t.Errorf("JWKS buscada %d vezes (quer 2)", p.JWKSHits())
	}
}

func TestDescobertaComOutroEmissor(t *testing.T) {
	p := oidctest.New(t)
	cfg := configDe(p)
	cfg.Issuer = strings.Replace(p.Issuer(), "127.0.0.1", "localhost", 1) // mesmo servidor, outro nome
	prov, _ := New(cfg, nil)
	if err := prov.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "se apresenta como") {
		t.Errorf("emissor diferente: %v", err)
	}
}

func TestConfiguracao(t *testing.T) {
	ok := Config{Issuer: "https://sso.exemplo/realms/patchd", ClientID: "patchd-painel", ClientSecret: "s",
		RedirectURL: "https://patchd.exemplo:8443/painel/oidc/retorno", RolesClaim: "realm_access.roles", AdminRole: "patchd-admin"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("válida: %v", err)
	}
	for nome, ajuste := range map[string]func(*Config){
		"issuer http fora do laboratório": func(c *Config) { c.Issuer = "http://sso.exemplo/realms/patchd" },
		"retorno em outro caminho":        func(c *Config) { c.RedirectURL = "https://patchd.exemplo:8443/painel/" },
		"sem segredo":                     func(c *Config) { c.ClientSecret = "" },
		"sem grupo admin":                 func(c *Config) { c.AdminRole = "" },
		"admin igual a leitura":           func(c *Config) { c.ReadRole = c.AdminRole },
		"claim malformado":                func(c *Config) { c.RolesClaim = "realm_access..roles" },
	} {
		c := ok
		ajuste(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: aceita", nome)
		}
	}
	lab := ok
	lab.Issuer = "http://127.0.0.1:18180/realms/patchd"
	if err := lab.Validate(); err != nil {
		t.Errorf("http no próprio host (laboratório): %v", err)
	}
	if got := RolesAt(map[string]any{"realm_access": map[string]any{"roles": []any{"a", "patchd-admin"}}}, "realm_access.roles"); len(got) != 2 || got[1] != "patchd-admin" {
		t.Errorf("RolesAt: %v", got)
	}
}

// Aula 19.4: os papéis da tela, lidos a cada login; o garantido sempre na lista de admin.
func TestPapeisDaTela(t *testing.T) {
	admin, read := []string{"patchd-admin", "/ti/infra"}, []string{"/gabinetes"}
	p := &Provider{cfg: Config{AdminRole: "patchd-admin", Roles: func() ([]string, []string) { return admin, read }}}
	claims := func(roles ...string) map[string]any {
		r := make([]any, len(roles))
		for i, x := range roles {
			r[i] = x
		}
		return map[string]any{"sub": "1", "preferred_username": "fulano", "groups": r}
	}
	p.cfg.RolesClaim = "groups"
	for want, roles := range map[string][]string{"admin": {"/ti/infra"}, "leitura": {"/gabinetes"}, "admin ": {"patchd-admin", "/gabinetes"}} {
		id, err := p.identity(claims(roles...))
		if err != nil || id.Role != strings.TrimSpace(want) {
			t.Errorf("%v: %q %v", roles, id.Role, err)
		}
	}
	read = nil
	if _, err := p.identity(claims("/gabinetes")); err == nil {
		t.Error("tirado da lista: sem perfil")
	}
}
