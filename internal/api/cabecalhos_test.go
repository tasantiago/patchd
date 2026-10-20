package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
)

// TestCabecalhosDeSeguranca (Aula 20.4): o que a varredura passiva do ZAP confere, aqui em
// todas as rotas do api.go (lidas pela matriz da 20.1), em toda resposta: página, JSON, erro,
// redirecionamento. Uma rota nova entra sozinha. As páginas HTML, além disso, não têm CSS nem
// script inline: a CSP delas não precisa de 'unsafe-inline'.
func TestCabecalhosDeSeguranca(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)

	confere := func(onde string, w *httptest.ResponseRecorder) {
		t.Helper()
		hd := w.Header()
		for k, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "same-origin",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Embedder-Policy": "require-corp",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if got := hd.Get(k); got != want {
				t.Errorf("%s (%d): %s = %q, esperava %q", onde, w.Code, k, got, want)
			}
		}
		if hd.Get("Permissions-Policy") == "" || hd.Get("Cache-Control") == "" {
			t.Errorf("%s (%d): sem Permissions-Policy ou Cache-Control", onde, w.Code)
		}
		csp := hd.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
			strings.Contains(csp, "unsafe-") || strings.Contains(csp, "*") {
			t.Errorf("%s (%d): CSP %q", onde, w.Code, csp)
		}
		if hd.Get("Server") != "" || hd.Get("X-Powered-By") != "" {
			t.Errorf("%s: o servidor se apresenta", onde)
		}
		// A página de verdade (200). O redirecionamento também é text/html (o Go põe um link no
		// corpo), mas fica com a CSP padrão, que é mais fechada.
		if w.Code == http.StatusOK && strings.HasPrefix(hd.Get("Content-Type"), "text/html") {
			b := strings.ToLower(w.Body.String())
			if strings.Contains(b, "<style") || strings.Contains(b, " style=") || strings.Contains(b, "<script") {
				t.Errorf("%s: página com CSS ou script inline", onde)
			}
			if !strings.Contains(csp, "style-src 'self'") || !strings.Contains(csp, "form-action 'self'") {
				t.Errorf("%s: CSP da página %q", onde, csp)
			}
		}
	}

	chama := func(metodo, caminho string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(metodo, "http://patchd.exemplo"+caminho, strings.NewReader("a=1"))
		if metodo == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for _, m := range mods {
			m(req)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	admin := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: sessaoAdmin}) }
	maquina := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+maq.Credential) }

	n := 0
	for _, r := range lerRotas(t) {
		c := caminhoDeTeste(r.Caminho)
		confere(r.Metodo+" "+r.Caminho+" sem nada", chama(r.Metodo, c))
		if r.Classe == clAgente {
			confere(r.Metodo+" "+r.Caminho+" com a máquina", chama(r.Metodo, c, maquina))
		} else {
			confere(r.Metodo+" "+r.Caminho+" com admin", chama(r.Metodo, c, admin))
		}
		n++
	}
	for _, c := range []string{"/painel/nao-existe", "/api/v1/nao-existe", "/painel/maquinas/" + strings.Repeat("z", 300)} {
		confere("GET "+c, chama("GET", c, admin))
	}
	if n < 60 {
		t.Errorf("só %d rotas: a leitura do api.go quebrou?", n)
	}
}

// TestEstiloComVersao: o CSS vem com cache longo só no endereço com a versão que a página
// aponta.
func TestEstiloComVersao(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	m := regexp.MustCompile(`href="(/painel/estilo\.css\?v=([0-9a-f]{16}))"`).FindStringSubmatch(pagina(h, "GET", "/painel/entrar", "", nil).Body.String())
	if m == nil {
		t.Fatal("a tela de login não aponta para o CSS com a versão")
	}
	w := pagina(h, "GET", m[1], "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/css; charset=utf-8" ||
		!strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Body.String(), ".w100{width:100%}") {
		t.Errorf("com a versão: %d %v", w.Code, w.Header())
	}
	if w := pagina(h, "GET", "/painel/estilo.css?v=velha", "", nil); w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("versão velha: %q", w.Header().Get("Cache-Control"))
	}
	if w := pagina(h, "GET", "/painel/estilo.css", "", nil, "If-None-Match", `"`+m[2]+`"`); w.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", w.Code)
	}
}
