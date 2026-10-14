package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/oidc/oidctest"
	"github.com/tasantiago/patchd/internal/panel"
)

// TestLoginPeloRHBK (Aula 9.4): botão → provedor → volta com o código → sessão "oidc".
func TestLoginPeloRHBK(t *testing.T) {
	p := oidctest.New(t)
	prov, err := oidc.New(oidc.Config{Issuer: p.Issuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "https://127.0.0.1:18443/painel/oidc/retorno", RolesClaim: "groups", AdminRole: "patchd-admin", ReadRole: "patchd-leitura"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, st, log := ambientePaginas(t, api.WithOIDC(prov))

	if b := pagina(h, "GET", "/painel/entrar", "", nil).Body.String(); !strings.Contains(b, `href="/painel/oidc/entrar"`) {
		t.Fatalf("botão no login:\n%s", b)
	}
	// inicia devolve o endereço do provedor e o cookie do state.
	inicia := func() (authURL, state string, c *http.Cookie) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/painel/oidc/entrar", nil))
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, p.Issuer()+"/protocol/openid-connect/auth?") {
			t.Fatalf("início: %d %s", rec.Code, loc)
		}
		u, _ := url.Parse(loc)
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == "patchd_oidc" {
				c = ck
			}
		}
		if c == nil || c.Value != u.Query().Get("state") || c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || c.Path != "/painel/oidc/" {
			t.Fatalf("cookie do state: %+v", c)
		}
		return loc, c.Value, c
	}
	volta := func(q string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/painel/oidc/retorno?"+q, nil)
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	auth, state, c := inicia()
	p.Authorize(auth, "Fulano.Lab", "patchd-admin")
	rec := volta("code="+oidctest.Code+"&state="+url.QueryEscape(state), c)
	var sessao *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "patchd_sessao" && ck.Value != "" {
			sessao = ck
		}
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `url=/painel/`) || sessao == nil || sessao.SameSite != http.SameSiteStrictMode {
		t.Fatalf("volta: %d %+v\n%s", rec.Code, sessao, rec.Body.String())
	}
	s, found, err := st.PanelSession(context.Background(), identity.Hash(sessao.Value))
	if err != nil || !found || s.Username != "fulano.lab" || s.Role != panel.RoleAdmin || s.Source != panel.SourceOIDC {
		t.Fatalf("sessão: %+v %v %v", s, found, err)
	}
	if b := pagina(h, "GET", "/painel/", sessao.Value, nil).Body.String(); !strings.Contains(b, "fulano.lab · RHBK") ||
		!strings.Contains(b, `<span class="ola">Olá, Nome de Fulano.Lab</span>`) {
		t.Errorf("painel com a sessão do RHBK:\n%s", b)
	}

	// A mesma volta de novo (state já usado), sem o cookie, ou com o cookie de outro login.
	if r := volta("code="+oidctest.Code+"&state="+url.QueryEscape(state), c); r.Code != http.StatusBadRequest {
		t.Errorf("state repetido: %d", r.Code)
	}
	_, state2, c2 := inicia()
	if r := volta("code="+oidctest.Code+"&state="+url.QueryEscape(state2), nil); r.Code != http.StatusBadRequest {
		t.Errorf("sem o cookie: %d", r.Code)
	}
	_, state3, _ := inicia()
	if r := volta("code="+oidctest.Code+"&state="+url.QueryEscape(state3), c2); r.Code != http.StatusBadRequest {
		t.Errorf("cookie de outro login: %d", r.Code)
	}
	// O provedor recusou (a pessoa cancelou, por exemplo).
	_, state4, c4 := inicia()
	if r := volta("error=access_denied&error_description=cancelado&state="+url.QueryEscape(state4), c4); r.Code != http.StatusUnauthorized ||
		!strings.Contains(r.Body.String(), "não concluiu") {
		t.Errorf("erro do provedor: %d", r.Code)
	}
	// Autenticado no provedor, mas sem grupo do painel: 403, com os grupos no log.
	auth5, state5, c5 := inicia()
	p.Authorize(auth5, "ciclano", "financeiro")
	if r := volta("code="+oidctest.Code+"&state="+url.QueryEscape(state5), c5); r.Code != http.StatusForbidden ||
		!strings.Contains(r.Body.String(), "nenhum grupo do painel") {
		t.Errorf("sem grupo: %d %s", r.Code, r.Body.String())
	}
	if !strings.Contains(log.String(), "sem grupo do painel") || !strings.Contains(log.String(), "financeiro") {
		t.Errorf("log:\n%s", log.String())
	}
	// Sem RHBK configurado: as rotas não existem e o botão some.
	h2, _, _ := ambientePaginas(t)
	if r := pagina(h2, "GET", "/painel/oidc/entrar", "", nil); r.Code != http.StatusNotFound {
		t.Errorf("sem RHBK: %d", r.Code)
	}
	if b := pagina(h2, "GET", "/painel/entrar", "", nil).Body.String(); strings.Contains(b, "/painel/oidc/") {
		t.Error("botão sem RHBK")
	}
}
