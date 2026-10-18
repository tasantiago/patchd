package userapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

// canal falso: responde a loja combinada e guarda os pedidos.
type canal struct {
	mu      sync.Mutex
	loja    localapi.Response
	pedido  localapi.Response
	err     error
	pedidos []localapi.Request
}

func (c *canal) call(_ context.Context, r localapi.Request) (localapi.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return localapi.Response{}, c.err
	}
	if r.Op == localapi.OpRequest {
		c.pedidos = append(c.pedidos, r)
		return c.pedido, nil
	}
	return c.loja, nil
}

func lojaDeTeste(openJob int64) localapi.Response {
	return localapi.Response{OK: true, Account: `EXEMPLO\teste`, Catalog: &protocol.SelfServiceCatalog{OpenJob: openJob, Items: []protocol.SelfServiceItem{
		{App: "7zip", Name: "7-Zip", Installed: "26.01", Target: "26.04", Action: "atualizar"},
		{App: "notepad-plus-plus", Name: "Notepad++", Installed: "8.9.1", Action: "em_dia"},
		{App: "vscode", Name: "Visual Studio Code", Action: "instalar"},
	}}}
}

func appDeTeste(c *canal) *App {
	a := New(c.call)
	a.Addr = "127.0.0.1:5555"
	a.Now = func() time.Time { return time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC) }
	return a
}

func get(a *App, path string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://127.0.0.1:5555"+path, nil)
	for _, m := range mod {
		m(r)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func post(a *App, path string, form url.Values, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:5555"+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestPaginaAtualizacoes(t *testing.T) {
	c := &canal{loja: lojaDeTeste(0)}
	a := appDeTeste(c)
	w := get(a, a.base())
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "7-Zip") || !strings.Contains(body, "26.01 → <b>26.04</b>") ||
		!strings.Contains(body, `name="app" value="7zip"`) || !strings.Contains(body, "Notepad&#43;&#43;") {
		t.Fatalf("página: %d\n%s", w.Code, body)
	}
	if strings.Contains(body, "Visual Studio Code") {
		t.Error("o que só pode ser instalado fica para a aba Loja (17.5), não em Atualizações")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script") {
		t.Errorf("CSP sem script: %q", csp)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Refresh") != "" {
		t.Errorf("cabeçalhos: %v", w.Header())
	}

	// Com instalação em andamento: aviso, botões desligados e a página se atualiza sozinha.
	c.loja = lojaDeTeste(51)
	w = get(a, a.base())
	if !strings.Contains(w.Body.String(), "pedido nº 51") || !strings.Contains(w.Body.String(), "disabled") || w.Header().Get("Refresh") != "15" {
		t.Errorf("em andamento: %v\n%s", w.Header(), w.Body.String())
	}

	// Erros do canal viram frases para o usuário.
	c.loja = localapi.Response{Code: localapi.CodeOff, Error: "o autoatendimento está desligado no servidor"}
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "O autoatendimento está desligado. Fale com o suporte.") {
		t.Errorf("desligado:\n%s", b)
	}
	c.err = localapi.ErrNoAgent
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "Não foi possível falar com o patchd") {
		t.Errorf("sem agente:\n%s", b)
	}
}

func TestSegredoHostOrigem(t *testing.T) {
	c := &canal{loja: lojaDeTeste(0), pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 52, Message: "Pedido aceito: atualização de 7-Zip 26.04."}}}
	a := appDeTeste(c)
	if w := get(a, "/"); w.Code != 404 {
		t.Errorf("sem o segredo: %d", w.Code)
	}
	if w := get(a, "/s/"+strings.Repeat("A", len(a.Token))+"/"); w.Code != 404 {
		t.Errorf("segredo errado: %d", w.Code)
	}
	// DNS rebinding: um site aponta o próprio nome para 127.0.0.1; o Host denuncia.
	if w := get(a, a.base(), func(r *http.Request) { r.Host = "ataque.exemplo:5555" }); w.Code != 404 {
		t.Errorf("Host de fora: %d", w.Code)
	}
	form := url.Values{"app": {"7zip"}, "action": {"atualizar"}}
	for _, o := range []string{"", "https://ataque.exemplo", "null"} {
		if w := post(a, a.base()+"pedir", form, o); w.Code != 403 {
			t.Errorf("POST com Origin %q: %d", o, w.Code)
		}
	}
	if len(c.pedidos) != 0 {
		t.Fatal("pedido de fora não chega ao agente")
	}

	// O formulário da própria página: pede, redireciona, e o aviso aparece uma vez.
	w := post(a, a.base()+"pedir", form, "http://127.0.0.1:5555")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != a.base() || len(c.pedidos) != 1 || c.pedidos[0].App != "7zip" || c.pedidos[0].Action != "atualizar" {
		t.Fatalf("pedido: %d %v %+v", w.Code, w.Header(), c.pedidos)
	}
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, `class="aviso sucesso"`) || !strings.Contains(b, "Pedido aceito") {
		t.Errorf("aviso do pedido:\n%s", b)
	}
	if b := get(a, a.base()).Body.String(); strings.Contains(b, "Pedido aceito") {
		t.Error("o aviso aparece uma vez só")
	}

	// Recusa e limite: avisos, não erro da página.
	c.pedido = localapi.Response{Code: localapi.CodeLimit, Error: "pedidos demais em pouco tempo; tente de novo em 7 minuto(s)"}
	post(a, a.base()+"pedir", form, "http://127.0.0.1:5555")
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "Pedidos demais em pouco tempo; tente de novo em 7 minuto(s).") {
		t.Errorf("limite:\n%s", b)
	}
	if w := get(a, a.base()+"pedir"); w.Code != 404 {
		t.Errorf("GET no pedido: %d", w.Code)
	}
}

func TestAvisoDoIcone(t *testing.T) {
	c := &canal{loja: lojaDeTeste(0)}
	a := appDeTeste(c)
	out := make(chan Notice, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Watch(ctx, time.Millisecond, 20*time.Millisecond, out)
	n := <-out
	if len(n.Updates) != 1 || n.Updates[0] != "7-Zip" {
		t.Fatalf("aviso: %+v", n)
	}
	select {
	case n := <-out:
		t.Fatalf("a mesma lista não é repetida: %+v", n)
	case <-time.After(70 * time.Millisecond):
	}
	title, text := noticeText(Notice{Updates: []string{"7-Zip", "Notepad++"}})
	if title != "Atualizações disponíveis" || !strings.Contains(text, "2 programas") {
		t.Errorf("balão: %q %q", title, text)
	}
	if _, text := noticeText(Notice{}); text != "" {
		t.Error("sem atualização, sem balão")
	}
}

func TestServeSoLocal(t *testing.T) {
	a := New((&canal{loja: lojaDeTeste(0)}).call)
	ln, err := a.Listen()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.URL(), "http://127.0.0.1:") || !strings.HasSuffix(a.URL(), a.base()) || len(a.Token) < 40 {
		t.Fatalf("URL: %s", a.URL())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	resp, err := http.Get(a.URL())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET: %v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
}
