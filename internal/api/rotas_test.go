package api_test

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Matriz de rotas (Aula 20.1, S4 do bloco de segurança). O teste lê o api.go como código
// (go/parser), acha cada mux.Handle e classifica a rota pela guarda que a envolve. Uma rota
// nova sem guarda conhecida, um POST do painel sem conferência de origem ou um formulário de
// admin sem adminAction quebram o teste antes de chegar ao servidor. Depois, cada rota é
// chamada de verdade: sem sessão, com a credencial de uma máquina, com sessão de leitura e,
// nos formulários, de outra origem. E a matriz em docs/rotas.md é gerada daqui (-update).

var atualizaMatriz = flag.Bool("update", false, "reescreve docs/rotas.md com a matriz de rotas")

// Classes de rota, pela guarda.
const (
	clPublica  = "pública"
	clForm     = "formulário público"
	clAgente   = "agente"
	clSessao   = "sessão (página)"
	clSessaoAP = "sessão (API)"
	clAdmin    = "admin (leitura)"
	clAdminAc  = "admin (ação)"
)

// publicas: as únicas rotas sem sessão nem credencial. Uma nova entra aqui de propósito, com o
// motivo, ou o teste quebra.
var publicas = map[string]string{
	"POST /api/v1/enroll":      "registro da máquina: exige um token de enrollment no corpo",
	"GET /painel/favicon.svg":  "ícone",
	"GET /painel/logo":         "logotipo da tela de login (Aula 19.1)",
	"GET /painel/entrar":       "tela de login",
	"POST /painel/entrar":      "login (conferência de origem, limite de tentativas)",
	"POST /painel/sair":        "saída (conferência de origem)",
	"GET /painel/oidc/entrar":  "início do login pelo RHBK (state, nonce e PKCE)",
	"GET /painel/oidc/retorno": "volta do RHBK (confere state, nonce e o ID token)",
}

type rota struct {
	Metodo, Caminho string
	Guardas         []string
	Classe          string
	Linha           int
}

// constantes do protocolo que aparecem nos caminhos.
var constantes = map[string]string{
	"OfflineCatalogPath":     protocol.OfflineCatalogPath,
	"PackagesPath":           protocol.PackagesPath,
	"JobResultPath":          protocol.JobResultPath,
	"SelfServiceCatalogPath": protocol.SelfServiceCatalogPath,
	"SelfServiceRequestPath": protocol.SelfServiceRequestPath,
	"SelfServiceLogoPath":    protocol.SelfServiceLogoPath,
}

func avaliaCaminho(t *testing.T, e ast.Expr) string {
	t.Helper()
	switch x := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			t.Fatal(err)
		}
		return s
	case *ast.BinaryExpr:
		return avaliaCaminho(t, x.X) + avaliaCaminho(t, x.Y)
	case *ast.SelectorExpr:
		if v, ok := constantes[x.Sel.Name]; ok {
			return v
		}
	}
	t.Fatalf("caminho que o teste não sabe ler: %T (acrescente a constante em 'constantes')", e)
	return ""
}

// guardas: a cadeia de envoltórios, de fora para dentro (a.sameOrigin(a.adminAction(h)) →
// sameOrigin, adminAction). staleAfter não é guarda.
func guardas(e ast.Expr) []string {
	var out []string
	for {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) != 1 {
			return out
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name != "staleAfter" {
			out = append(out, sel.Sel.Name)
		}
		e = c.Args[0]
	}
}

func classe(g []string) string {
	switch strings.Join(g, ">") {
	case "":
		return clPublica
	case "sameOrigin":
		return clForm
	case "machineAuth":
		return clAgente
	case "panelPage":
		return clSessao
	case "sessionAuth":
		return clSessaoAP
	case "adminAction":
		return clAdmin
	case "sameOrigin>adminAction":
		return clAdminAc
	}
	return ""
}

func lerRotas(t *testing.T) []rota {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []rota
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || len(c.Args) != 2 || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "mux" {
			return true
		}
		pat := avaliaCaminho(t, c.Args[0])
		metodo, caminho, ok := strings.Cut(pat, " ")
		if !ok {
			t.Errorf("%s: rota sem método (%q): toda rota declara GET ou POST", fs.Position(c.Pos()), pat)
			return true
		}
		g := guardas(c.Args[1])
		out = append(out, rota{Metodo: metodo, Caminho: caminho, Guardas: g, Classe: classe(g), Linha: fs.Position(c.Pos()).Line})
		return true
	})
	if len(out) < 50 {
		t.Fatalf("só %d rotas lidas: o api.go mudou de forma?", len(out))
	}
	return out
}

// TestMatrizDeRotas: as regras, só olhando o código.
func TestMatrizDeRotas(t *testing.T) {
	rotas := lerRotas(t)
	vistas := map[string]bool{}
	for _, r := range rotas {
		chave := r.Metodo + " " + r.Caminho
		if vistas[chave] {
			t.Errorf("api.go:%d: %s registrada duas vezes", r.Linha, chave)
		}
		vistas[chave] = true
		_, publica := publicas[chave]
		switch {
		case r.Classe == "":
			t.Errorf("api.go:%d: %s com guarda desconhecida %v: use machineAuth, panelPage, sessionAuth, adminAction ou sameOrigin(adminAction)", r.Linha, chave, r.Guardas)
		case (r.Classe == clPublica || r.Classe == clForm) && !publica:
			t.Errorf("api.go:%d: %s ficou sem sessão nem credencial: se é de propósito, ponha em 'publicas' com o motivo", r.Linha, chave)
		case strings.HasPrefix(r.Caminho, "/api/v1/agent/") && r.Classe != clAgente:
			t.Errorf("api.go:%d: %s é da API do agente e não exige a credencial da máquina", r.Linha, chave)
		case r.Metodo == "POST" && strings.HasPrefix(r.Caminho, "/painel/") && !publica && r.Classe != clAdminAc:
			t.Errorf("api.go:%d: %s muda alguma coisa no painel sem sameOrigin(adminAction(...))", r.Linha, chave)
		case strings.HasPrefix(r.Caminho, "/painel/configuracao") && r.Classe != clAdmin && r.Classe != clAdminAc:
			t.Errorf("api.go:%d: %s é da Configuração e não é só de admin", r.Linha, chave)
		case r.Metodo != "GET" && r.Metodo != "POST":
			t.Errorf("api.go:%d: %s: só GET e POST (o painel não usa outros métodos)", r.Linha, chave)
		}
	}
	for k := range publicas {
		if !vistas[k] {
			t.Errorf("'publicas' cita %s, que não existe mais", k)
		}
	}
	matriz := matrizMarkdown(rotas)
	const arquivo = "../../docs/rotas.md"
	if *atualizaMatriz {
		if err := os.WriteFile(arquivo, []byte(matriz), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	atual, err := os.ReadFile(arquivo)
	if err != nil || string(atual) != matriz {
		t.Errorf("docs/rotas.md desatualizada: rode go test ./internal/api -run TestMatrizDeRotas -update e faça o commit junto")
	}
}

var quemPode = map[string]string{
	clPublica: "qualquer um", clForm: "qualquer um, só da própria origem", clAgente: "máquina registrada (credencial)",
	clSessao: "sessão do painel (leitura ou admin)", clSessaoAP: "sessão do painel (leitura ou admin)",
	clAdmin: "sessão de admin", clAdminAc: "sessão de admin, só da própria origem",
}

var semSessao = map[string]string{
	clPublica: "atende", clForm: "atende", clAgente: "401", clSessao: "303 → /painel/entrar", clSessaoAP: "401",
	clAdmin: "303 → /painel/entrar", clAdminAc: "303 → /painel/entrar",
}

func matrizMarkdown(rotas []rota) string {
	var b strings.Builder
	b.WriteString("# Matriz de rotas\n\n")
	b.WriteString("Gerada pelo teste `TestMatrizDeRotas` (internal/api/rotas_test.go) a partir do `api.go`: não edite à mão.\n")
	b.WriteString("Para atualizar: `go test ./internal/api -run TestMatrizDeRotas -update`. O teste `TestMatrizDeRotasNaPratica`\n")
	b.WriteString("chama cada rota sem sessão, com a credencial de uma máquina, com sessão de leitura e, nos formulários,\n")
	b.WriteString("de outra origem. As rotas do processo (`/healthz`, `/repo/`, o proxy do apt e a porta de métricas)\n")
	b.WriteString("ficam em cmd/server e estão no modelo de ameaças (docs/seguranca.md).\n\n")
	ordem := []string{clPublica, clForm, clAgente, clSessao, clSessaoAP, clAdmin, clAdminAc}
	for _, c := range ordem {
		var l []rota
		for _, r := range rotas {
			if r.Classe == c {
				l = append(l, r)
			}
		}
		if len(l) == 0 {
			continue
		}
		slices.SortFunc(l, func(a, b rota) int { return strings.Compare(a.Caminho+a.Metodo, b.Caminho+b.Metodo) })
		fmt.Fprintf(&b, "## %s (%d)\n\nQuem pode: %s. Sem sessão: %s.", strings.ToUpper(c[:1])+c[1:], len(l), quemPode[c], semSessao[c])
		if c == clAdmin || c == clAdminAc {
			b.WriteString(" Sessão de leitura: 403, com o evento acesso.negado na auditoria.")
		}
		if c == clForm || c == clAdminAc {
			b.WriteString(" De outra origem: 403.")
		}
		b.WriteString("\n\n| Método | Caminho |")
		if c == clPublica || c == clForm {
			b.WriteString(" Por que é aberta |")
		}
		b.WriteString("\n|---|---|")
		if c == clPublica || c == clForm {
			b.WriteString("---|")
		}
		b.WriteString("\n")
		for _, r := range l {
			fmt.Fprintf(&b, "| %s | `%s` |", r.Metodo, r.Caminho)
			if c == clPublica || c == clForm {
				fmt.Fprintf(&b, " %s |", publicas[r.Metodo+" "+r.Caminho])
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

var parametro = regexp.MustCompile(`\{[a-z0-9]+\}`)

// caminhoDeTeste troca os parâmetros por valores plausíveis.
func caminhoDeTeste(p string) string {
	p = strings.ReplaceAll(p, "{$}", "")
	return parametro.ReplaceAllStringFunc(p, func(s string) string {
		switch s {
		case "{sha256}":
			return strings.Repeat("ab", 32)
		case "{version}":
			return "v0.0.1"
		case "{ring}":
			return "1"
		case "{op}":
			return "pausar"
		case "{name}":
			return "fulano"
		case "{id}":
			return "60ef97e4-0001"
		}
		return "x"
	})
}

// TestMatrizDeRotasNaPratica: cada rota, chamada de verdade, recusa quem não pode.
func TestMatrizDeRotasNaPratica(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)

	chama := func(r rota, mods ...func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(r.Metodo, "http://patchd.exemplo"+caminhoDeTeste(r.Caminho), strings.NewReader("a=1"))
		req.Host = "patchd.exemplo"
		if r.Metodo == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for _, m := range mods {
			m(req)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	cookie := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: v}) }
	}
	maquina := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+maq.Credential) }
	deFora := func(r *http.Request) {
		r.Header.Set("Origin", "https://atacante.exemplo")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	}
	recusa := func(r rota, quem string, w *httptest.ResponseRecorder, codigos ...int) {
		t.Helper()
		if !slices.Contains(codigos, w.Code) {
			t.Errorf("%s %s (%s) %s: %d, esperava %v", r.Metodo, r.Caminho, r.Classe, quem, w.Code, codigos)
		}
		if w.Code == http.StatusSeeOther && w.Header().Get("Location") != "/painel/entrar" {
			t.Errorf("%s %s %s: redirecionou para %q", r.Metodo, r.Caminho, quem, w.Header().Get("Location"))
		}
	}
	for _, r := range lerRotas(t) {
		switch r.Classe {
		case clAgente:
			recusa(r, "sem credencial", chama(r), 401)
			recusa(r, "com a sessão de admin no lugar da credencial", chama(r, cookie(sessaoAdmin)), 401)
			if w := chama(r, maquina); w.Code == 401 || w.Code == 403 {
				t.Errorf("%s %s: a credencial da máquina foi recusada: %d", r.Metodo, r.Caminho, w.Code)
			}
		case clSessao:
			recusa(r, "sem sessão", chama(r), 303)
			recusa(r, "com a credencial da máquina", chama(r, maquina), 303)
			if w := chama(r, cookie(sessaoTeste)); w.Code == 303 || w.Code == 401 || w.Code == 403 {
				t.Errorf("%s %s: a sessão de leitura foi recusada: %d", r.Metodo, r.Caminho, w.Code)
			}
		case clSessaoAP:
			recusa(r, "sem sessão", chama(r), 401)
			recusa(r, "com a credencial da máquina", chama(r, maquina), 401)
			if w := chama(r, cookie(sessaoTeste)); w.Code == 401 || w.Code == 403 {
				t.Errorf("%s %s: a sessão de leitura foi recusada: %d", r.Metodo, r.Caminho, w.Code)
			}
		case clAdmin, clAdminAc:
			recusa(r, "sem sessão", chama(r), 303)
			recusa(r, "com a credencial da máquina", chama(r, maquina), 303)
			recusa(r, "com sessão de leitura", chama(r, cookie(sessaoTeste)), 403)
			if r.Classe == clAdminAc {
				recusa(r, "de outra origem, com sessão de admin", chama(r, cookie(sessaoAdmin), deFora), 403)
			}
		case clForm:
			recusa(r, "de outra origem", chama(r, deFora), 403)
		}
	}
	// A sessão de admin continua valendo: nenhuma das chamadas acima a derrubou nem mudou nada
	// que a leitura não pudesse mudar.
	if w := chama(rota{Metodo: "GET", Caminho: "/painel/configuracao"}, cookie(sessaoAdmin)); w.Code != 200 {
		t.Errorf("admin depois das tentativas: %d", w.Code)
	}
}
