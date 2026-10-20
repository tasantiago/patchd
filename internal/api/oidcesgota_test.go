package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/oidc/oidctest"
)

// TestLoginRHBKNaoEsgota (Aula 20.4b, achado F7 da varredura ativa do ZAP): o início do
// login pelo RHBK não tem autenticação e guarda o login em andamento por 10 minutos. Antes,
// mil pedidos de qualquer um enchiam a tabela e todo login pelo RHBK recebia 503 até vencer.
// Agora cada IP guarda poucos (o mais velho dele sai) e, com a tabela cheia, sai o mais velho
// de todos: ninguém é recusado.
func TestLoginRHBKNaoEsgota(t *testing.T) {
	p := oidctest.New(t)
	prov, err := oidc.New(oidc.Config{Issuer: p.Issuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "https://127.0.0.1:18443/painel/oidc/retorno", RolesClaim: "groups", AdminRole: "patchd-admin", ReadRole: "patchd-leitura"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, _, log := ambientePaginas(t, api.WithOIDC(prov))
	inicia := func(ip string) (*httptest.ResponseRecorder, *http.Cookie) {
		req := httptest.NewRequest("GET", "/painel/oidc/entrar", nil)
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "patchd_oidc" {
				return rec, c
			}
		}
		return rec, nil
	}
	volta := func(state string, c *http.Cookie) int {
		req := httptest.NewRequest("GET", "/painel/oidc/retorno?code="+oidctest.Code+"&state="+url.QueryEscape(state), nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Um IP só, 1500 inícios (o ZAP faz isso numa varredura ativa).
	type inicio struct {
		loc string
		c   *http.Cookie
	}
	var primeiro, ultimo inicio
	for i := 0; i < 1500; i++ {
		rec, c := inicia("203.0.113.66")
		if rec.Code != http.StatusSeeOther || c == nil {
			t.Fatalf("início %d do mesmo IP: %d", i, rec.Code)
		}
		ultimo = inicio{rec.Header().Get("Location"), c}
		if i == 0 {
			primeiro = ultimo
		}
	}
	// Quem chega depois, de outro IP, entra.
	rec, c := inicia("198.51.100.7")
	if rec.Code != http.StatusSeeOther || c == nil {
		t.Fatalf("outro IP depois da enxurrada: %d", rec.Code)
	}
	p.Authorize(rec.Header().Get("Location"), "Fulano.Lab", "patchd-admin")
	if code := volta(c.Value, c); code != http.StatusOK {
		t.Fatalf("login de outro IP: %d", code)
	}
	// Do IP da enxurrada, o último ainda vale e o primeiro saiu.
	p.Authorize(ultimo.loc, "Fulano.Lab", "patchd-admin")
	if code := volta(ultimo.c.Value, ultimo.c); code != http.StatusOK {
		t.Errorf("o último do mesmo IP: %d", code)
	}
	p.Authorize(primeiro.loc, "Fulano.Lab", "patchd-admin")
	if code := volta(primeiro.c.Value, primeiro.c); code != http.StatusBadRequest {
		t.Errorf("o primeiro do mesmo IP deveria ter saído: %d", code)
	}
	if strings.Contains(log.String(), "no limite") {
		t.Error("um IP só não enche a tabela")
	}
}

// Muitos IPs (a tabela inteira): sai o mais velho de todos, e o novo entra.
func TestLoginRHBKTabelaCheia(t *testing.T) {
	p := oidctest.New(t)
	prov, _ := oidc.New(oidc.Config{Issuer: p.Issuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "https://127.0.0.1:18443/painel/oidc/retorno", RolesClaim: "groups", AdminRole: "patchd-admin", ReadRole: "patchd-leitura"}, nil)
	h, _, log := ambientePaginas(t, api.WithOIDC(prov))
	for i := 0; i < 1200; i++ {
		req := httptest.NewRequest("GET", "/painel/oidc/entrar", nil)
		req.RemoteAddr = "10.9." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250) + ":40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("início %d: %d", i, rec.Code)
		}
	}
	if n := strings.Count(log.String(), "logins pelo RHBK em andamento no limite"); n != 1 {
		t.Errorf("o aviso sai uma vez por minuto, não a cada pedido: %d", n)
	}
}
