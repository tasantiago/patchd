package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

const senhaAdmin = "senha-de-teste-longa"

// syncBuffer: o log é escrito por quem atende e lido pelo teste.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// ambientePainel: API sobre a memória com o usuário admin "thyago".
func ambientePainel(t *testing.T) (http.Handler, *store.Memory, *syncBuffer) {
	t.Helper()
	st := store.NewMemory()
	hash, err := panel.HashPassword(senhaAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePanelUser(context.Background(), panel.User{Name: "thyago", Role: panel.RoleAdmin, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	return api.New(st, slog.New(slog.NewTextHandler(log, nil))), st, log
}

// entra envia o formulário de login a partir do IP informado.
func entra(h http.Handler, usuario, senha, ip string, cabecalhos ...string) *httptest.ResponseRecorder {
	form := url.Values{"usuario": {usuario}, "senha": {senha}}
	req := httptest.NewRequest("POST", "/painel/entrar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = ip + ":40000"
	for i := 0; i+1 < len(cabecalhos); i += 2 {
		req.Header.Set(cabecalhos[i], cabecalhos[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func comCookie(h http.Handler, method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieDaSessao(t *testing.T, r *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range r.Result().Cookies() {
		if c.Name == "patchd_sessao" {
			return c
		}
	}
	t.Fatalf("sem cookie de sessão: %v", r.Header())
	return nil
}

func TestLeituraExigeSessao(t *testing.T) {
	st := store.NewMemory()
	h := api.New(st, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	agora := time.Now().UTC()
	abreSessao(t, st, panel.SessionPrefix+"valida", agora.Add(-2*time.Minute))
	abreSessao(t, st, panel.SessionPrefix+"ociosa", agora.Add(-31*time.Minute))
	vencida := panel.SessionPrefix + "vencida"
	if err := st.CreatePanelSession(context.Background(), identity.Hash(vencida), panel.Session{Username: "x", Role: panel.RoleRead,
		Source: panel.SourceLocal, CreatedAt: agora.Add(-9 * time.Hour), ExpiresAt: agora.Add(-time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}

	rotas := []string{"/api/v1/machines", "/api/v1/machines/qualquer/inventory", "/api/v1/machines/qualquer/scan", "/api/v1/identity-links"}
	for _, rota := range rotas {
		for _, c := range []*http.Cookie{
			nil,
			{Name: "patchd_sessao", Value: "lixo"},
			{Name: "patchd_sessao", Value: panel.SessionPrefix + "desconhecida"},
			{Name: "patchd_sessao", Value: panel.SessionPrefix + "ociosa"},
			{Name: "patchd_sessao", Value: vencida},
		} {
			r := comCookie(h, "GET", rota, c)
			if r.Code != http.StatusUnauthorized || !strings.Contains(r.Body.String(), `"unauthorized"`) {
				t.Errorf("%s com %v: %d %s", rota, c, r.Code, r.Body)
			}
		}
	}
	// As vencidas foram apagadas na primeira consulta.
	for _, c := range []string{panel.SessionPrefix + "ociosa", vencida} {
		if _, found, _ := st.PanelSession(context.Background(), identity.Hash(c)); found {
			t.Errorf("%s deveria ter sido apagada", c)
		}
	}

	valida := &http.Cookie{Name: "patchd_sessao", Value: panel.SessionPrefix + "valida"}
	r := comCookie(h, "GET", "/api/v1/machines", valida)
	if r.Code != http.StatusOK || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("sessão válida: %d %v", r.Code, r.Header())
	}
	// O uso foi registrado (passou mais de 1 min desde o último).
	s, _, _ := st.PanelSession(context.Background(), identity.Hash(valida.Value))
	if time.Since(s.LastSeenAt) > 10*time.Second {
		t.Errorf("último uso não atualizado: %v", s.LastSeenAt)
	}
	// As rotas do agente continuam na credencial da máquina: o cookie não serve lá.
	if r := comCookie(h, "POST", "/api/v1/agent/checkin", valida); r.Code != http.StatusUnauthorized {
		t.Errorf("cookie do painel na rota do agente: %d", r.Code)
	}
}

func TestLoginESaida(t *testing.T) {
	h, st, log := ambientePainel(t)

	// Sem sessão, o painel manda para o login.
	r := comCookie(h, "GET", "/painel/", nil)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/entrar" {
		t.Fatalf("painel sem sessão: %d %v", r.Code, r.Header())
	}
	r = comCookie(h, "GET", "/painel/entrar", nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `name="senha" type="password"`) ||
		r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("formulário: %d %v", r.Code, r.Header())
	}

	// Senha errada, usuário inexistente e senha vazia: a mesma resposta.
	for _, c := range [][2]string{{"thyago", "senha-errada-qualquer"}, {"ninguem", senhaAdmin}, {"thyago", ""}} {
		r := entra(h, c[0], c[1], "198.51.100.7")
		if r.Code != http.StatusUnauthorized || !strings.Contains(r.Body.String(), "Usuário ou senha inválidos.") || len(r.Result().Cookies()) != 0 {
			t.Errorf("%v: %d %s", c, r.Code, r.Body)
		}
	}

	// Nome com maiúsculas e espaços é o mesmo usuário.
	r = entra(h, "  Thyago ", senhaAdmin, "198.51.100.7")
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/" {
		t.Fatalf("login: %d %s", r.Code, r.Body)
	}
	c := cookieDaSessao(t, r)
	if !strings.HasPrefix(c.Value, panel.SessionPrefix) || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie: %+v (Secure só com TLS)", c)
	}
	s, found, _ := st.PanelSession(context.Background(), identity.Hash(c.Value))
	if !found || s.Username != "thyago" || s.Role != panel.RoleAdmin || s.Source != panel.SourceLocal ||
		s.ExpiresAt.Sub(s.CreatedAt) != panel.SessionMaxAge {
		t.Fatalf("sessão gravada pelo hash: %+v %t", s, found)
	}

	r = comCookie(h, "GET", "/painel/", c)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "thyago (local)") || !strings.Contains(r.Body.String(), "admin") {
		t.Errorf("painel: %d %s", r.Code, r.Body)
	}
	if r := comCookie(h, "GET", "/painel/entrar", c); r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/" {
		t.Errorf("login com sessão aberta vai ao painel: %d", r.Code)
	}
	if r := comCookie(h, "GET", "/api/v1/machines", c); r.Code != http.StatusOK {
		t.Errorf("leitura com a sessão: %d", r.Code)
	}

	// Sair encerra a sessão no servidor: o mesmo cookie, reapresentado, não vale mais.
	r = comCookie(h, "POST", "/painel/sair", c)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/entrar" {
		t.Fatalf("sair: %d", r.Code)
	}
	if apagado := cookieDaSessao(t, r); apagado.MaxAge >= 0 || apagado.Value != "" {
		t.Errorf("o cookie deveria ser apagado: %+v", apagado)
	}
	if r := comCookie(h, "GET", "/api/v1/machines", c); r.Code != http.StatusUnauthorized {
		t.Errorf("cookie depois de sair: %d", r.Code)
	}

	// O log registra quem entrou e quem errou, nunca a senha nem o segredo da sessão.
	l := log.String()
	for _, proibido := range []string{senhaAdmin, "senha-errada-qualquer", c.Value} {
		if strings.Contains(l, proibido) {
			t.Errorf("o log contém %q", proibido)
		}
	}
	if !strings.Contains(l, "login no painel") || !strings.Contains(l, "login recusado") || !strings.Contains(l, "saída do painel") {
		t.Errorf("log incompleto:\n%s", l)
	}
}

func TestLoginLimiteDeTentativas(t *testing.T) {
	h, _, log := ambientePainel(t)
	for i := 0; i < 5; i++ {
		if r := entra(h, "thyago", "errada-numero-"+string(rune('a'+i)), "203.0.113.5"); r.Code != http.StatusUnauthorized {
			t.Fatalf("tentativa %d: %d", i+1, r.Code)
		}
	}
	// A sexta é bloqueada mesmo com a senha certa.
	if r := entra(h, "thyago", senhaAdmin, "203.0.113.5"); r.Code != http.StatusTooManyRequests || len(r.Result().Cookies()) != 0 {
		t.Fatalf("bloqueio: %d", r.Code)
	}
	// De outro IP, o mesmo usuário entra.
	if r := entra(h, "thyago", senhaAdmin, "203.0.113.6"); r.Code != http.StatusSeeOther {
		t.Errorf("outro IP: %d", r.Code)
	}
	if !strings.Contains(log.String(), "tentativas demais") {
		t.Error("o bloqueio vai para o log")
	}

	// Muitos nomes diferentes do mesmo IP: o limite por IP (20) bloqueia o resto.
	h2, _, _ := ambientePainel(t)
	for i := 0; i < 20; i++ {
		entra(h2, "nome"+string(rune('a'+i)), "", "203.0.113.9")
	}
	if r := entra(h2, "thyago", senhaAdmin, "203.0.113.9"); r.Code != http.StatusTooManyRequests {
		t.Errorf("limite por IP: %d", r.Code)
	}
}

func TestLoginDeOutraOrigem(t *testing.T) {
	h, _, _ := ambientePainel(t)
	// Senha vazia: chega ao 401 sem gastar o PBKDF2.
	for _, c := range [][]string{
		{"Origin", "https://atacante.exemplo"},
		{"Sec-Fetch-Site", "cross-site"},
	} {
		if r := entra(h, "thyago", "", "192.0.2.1", c...); r.Code != http.StatusForbidden {
			t.Errorf("%v: %d", c, r.Code)
		}
	}
	for _, c := range [][]string{
		{"Origin", "http://example.com"}, // o Host do httptest
		{"Sec-Fetch-Site", "same-origin"},
		{}, // curl: sem cabeçalho de navegador
	} {
		if r := entra(h, "thyago", "", "192.0.2.1", c...); r.Code != http.StatusUnauthorized {
			t.Errorf("%v: %d", c, r.Code)
		}
	}
	// Sair também é protegido (um site alheio não encerra a sessão de ninguém).
	req := httptest.NewRequest("POST", "/painel/sair", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("sair de outra origem: %d", rec.Code)
	}
}

func TestLoginEscapaOHTML(t *testing.T) {
	h, _, log := ambientePainel(t)
	r := entra(h, `<script>alert(1)</script>`, "x", "192.0.2.1")
	if strings.Contains(r.Body.String(), "<script>") || !strings.Contains(r.Body.String(), "&lt;script&gt;") {
		t.Errorf("o nome digitado volta escapado:\n%s", r.Body)
	}
	if strings.Contains(log.String(), "script") {
		t.Error("nome fora do padrão não vai para o log")
	}
}
