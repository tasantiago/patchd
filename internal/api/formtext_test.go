package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

// TestFormularioComTextoInvalido (Aula 20.4b): NUL ou UTF-8 inválido em qualquer campo de
// formulário do painel é 400 na entrada, antes do handler (antes: 500 no revogar).
func TestFormularioComTextoInvalido(t *testing.T) {
	h, _, log := ambientePaginas(t)
	for nome, campos := range map[string]url.Values{
		"NUL":            {"id": {"curl\x00"}, "fix": {"8.18.0"}},
		"UTF-8 inválido": {"id": {"curl"}, "fix": {"8.18\xc3\x28"}},
		"na chave":       {"id\x00": {"curl"}},
	} {
		r := pagina(h, "POST", "/painel/patch/revogar", sessaoAdmin, campos)
		if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "caracteres inválidos") {
			t.Errorf("%s: %d %s", nome, r.Code, r.Body.String())
		}
	}
	// Na URL também.
	if r := pagina(h, "POST", "/painel/patch/revogar?x=%00", sessaoAdmin, url.Values{"id": {"curl"}}); r.Code != http.StatusBadRequest {
		t.Errorf("query: %d", r.Code)
	}
	// O login também passa por aqui.
	if r := pagina(h, "POST", "/painel/entrar", "", url.Values{"usuario": {"admin\x00"}, "senha": {"x"}}); r.Code != http.StatusBadRequest {
		t.Errorf("login: %d", r.Code)
	}
	if !strings.Contains(log.String(), "formulário do painel recusado") {
		t.Error("a recusa vai para o log")
	}
	// Formulário enorme: 413, sem ler tudo.
	req := httptest.NewRequest("POST", "/painel/patch/revogar", strings.NewReader("id="+strings.Repeat("a", 2<<20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: sessaoAdmin})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("enorme: %d", rec.Code)
	}
	// Texto válido com acento segue para o handler (que responde como antes).
	if r := pagina(h, "POST", "/painel/patch/revogar", sessaoAdmin, url.Values{"id": {"ação"}, "fix": {"1"}}); r.Code == http.StatusBadRequest &&
		strings.Contains(r.Body.String(), "caracteres inválidos") {
		t.Error("acento recusado")
	}
}

// TestAcessoNegadoComCaminhoInvalido (Aula 20.4b, achado F8): o perfil leitura pedindo uma
// rota de admin com %00 no caminho fica na trilha (o store troca o NUL por "�"; no
// PostgreSQL, antes, o INSERT falhava e a tentativa sumia: TestAuditoriaComTextoInvalido).
func TestAcessoNegadoComCaminhoInvalido(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	if r := pagina(h, "GET", "/painel/configuracao/agente/arquivo/x%00", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Fatalf("leitura numa rota de admin: %d", r.Code)
	}
	l, err := st.AuditEvents(context.Background(), store.AuditFilter{Action: "acesso.negado"})
	if err != nil || len(l) != 1 || l[0].Target != "/painel/configuracao/agente/arquivo/x\uFFFD" {
		t.Errorf("%+v %v", l, err)
	}
}
