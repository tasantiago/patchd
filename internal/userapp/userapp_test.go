package userapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	resp := c.loja // cópia: o teste muda a loja enquanto o app lê
	if resp.Catalog != nil {
		cat := *resp.Catalog
		cat.Items, cat.Requests = slices.Clone(cat.Items), slices.Clone(cat.Requests)
		resp.Catalog = &cat
	}
	return resp, nil
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
	if w.Header().Get("Referrer-Policy") != "same-origin" || w.Header().Get("Refresh") != "" {
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
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != a.base()+"?aba=pedidos" || len(c.pedidos) != 1 || c.pedidos[0].App != "7zip" || c.pedidos[0].Action != "atualizar" {
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

// Aula 17.5: a Loja (instalar, com confirmação) e Meus pedidos.
func TestLojaEMeusPedidos(t *testing.T) {
	loja := lojaDeTeste(0)
	fim := time.Date(2026, 10, 17, 12, 5, 0, 0, time.UTC)
	loja.Catalog.Items = append(loja.Catalog.Items,
		protocol.SelfServiceItem{App: "powershell-7", Name: "PowerShell 7", Installed: "7.5.3", Action: "instalado"},
		protocol.SelfServiceItem{App: "google-chrome", Name: "Google Chrome", Action: "indisponivel", Reason: "sem instalador aprovado para esta máquina"})
	loja.Catalog.Items[2].Target = "1.105.1"
	loja.Catalog.Requests = []protocol.SelfServiceJob{
		{ID: 60, App: "vscode", Name: "Visual Studio Code", Version: "1.105.1", State: "entregue", CreatedAt: fim},
		{ID: 58, App: "7zip", Name: "7-Zip", Version: "26.04", State: "adiado", Detail: "7zFM.exe em uso", CreatedAt: fim.Add(-time.Hour), FinishedAt: &fim},
		{ID: 55, App: "7zip", Name: "7-Zip", Version: "26.04", State: "concluido", Detail: "instalado; código 0", CreatedAt: fim.Add(-2 * time.Hour)},
	}
	c := &canal{loja: loja, pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 61, Message: "Pedido aceito: instalação de Visual Studio Code 1.105.1."}}}
	a := appDeTeste(c)
	a.Loc = time.UTC

	w := get(a, a.base()+"?aba=loja")
	b := w.Body.String()
	if !strings.Contains(b, "Visual Studio Code") || !strings.Contains(b, "confirmar=vscode") || strings.Contains(b, `value="instalar"`) ||
		!strings.Contains(b, "PowerShell 7") || !strings.Contains(b, "sem instalador aprovado") || strings.Contains(b, "26.01 → <b>26.04</b>") {
		t.Fatalf("loja: o botão leva à confirmação, não pede direto:\n%s", b)
	}
	if !strings.Contains(b, `class="conta">1</span>`) {
		t.Error("a aba Atualizações mostra quantas há")
	}
	b = get(a, a.base()+"?aba=loja&confirmar=vscode").Body.String()
	if !strings.Contains(b, "Instalar Visual Studio Code?") || !strings.Contains(b, `name="action" value="instalar"`) {
		t.Fatalf("confirmação:\n%s", b)
	}
	if b := get(a, a.base()+"?aba=loja&confirmar=7zip").Body.String(); strings.Contains(b, "Instalar 7-Zip?") {
		t.Error("só se confirma o que a loja oferece para instalar")
	}
	w = post(a, a.base()+"pedir", url.Values{"app": {"vscode"}, "action": {"instalar"}}, "http://127.0.0.1:5555")
	if w.Header().Get("Location") != a.base()+"?aba=pedidos" || c.pedidos[0].Action != "instalar" {
		t.Fatalf("instalar: %v %+v", w.Header(), c.pedidos)
	}
	// Recusado: volta para a Loja, com o aviso.
	c.pedido = localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "aguarde", Message: "Já há uma instalação em andamento."}}
	if w := post(a, a.base()+"pedir", url.Values{"app": {"vscode"}, "action": {"instalar"}}, "http://127.0.0.1:5555"); w.Header().Get("Location") != a.base()+"?aba=loja" {
		t.Errorf("aguarde volta para a loja: %v", w.Header())
	}

	w = get(a, a.base()+"?aba=pedidos")
	b = w.Body.String()
	for _, quer := range []string{"pedido nº 60 · 17/10 12:05", "Instalando", "Adiado", "7zFM.exe em uso", "Concluído"} {
		if !strings.Contains(b, quer) {
			t.Errorf("meus pedidos sem %q:\n%s", quer, b)
		}
	}
	if strings.Contains(b, "código 0") {
		t.Error("o detalhe do concluído não precisa aparecer")
	}
	if w.Header().Get("Refresh") != "15" {
		t.Error("com pedido instalando, a página se atualiza sozinha")
	}
}

// Aula 17.5: o pedido aceito antecipa a conferência, e o fim dele vira aviso no ícone.
func TestAvisoDePedidoConcluido(t *testing.T) {
	loja := lojaDeTeste(0)
	loja.Catalog.Items = loja.Catalog.Items[1:] // sem atualização: o único aviso é o do pedido
	c := &canal{loja: loja, pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 70, Message: "ok"}}}
	a := appDeTeste(c)
	a.fast = 10 * time.Millisecond
	out := make(chan Notice, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Watch(ctx, time.Hour, time.Hour, out) // sem o pedido, só daqui a uma hora

	setReq := func(state string) {
		c.mu.Lock()
		c.loja.Catalog.Requests = []protocol.SelfServiceJob{{ID: 70, Name: "Visual Studio Code", Version: "1.105.1", State: state}}
		c.mu.Unlock()
	}
	setReq("pendente")
	post(a, a.base()+"pedir", url.Values{"app": {"vscode"}, "action": {"instalar"}}, "http://127.0.0.1:5555")
	time.Sleep(30 * time.Millisecond) // conferências rápidas enquanto o pedido está na fila
	setReq("concluido")
	select {
	case n := <-out:
		title, text := noticeText(n)
		if title != "Pedido concluído" || text != "Visual Studio Code 1.105.1: pronto." {
			t.Errorf("aviso: %q %q", title, text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o fim do pedido não virou aviso")
	}
	if _, text := noticeText(Notice{Done: []string{doneText(protocol.SelfServiceJob{Name: "7-Zip", Version: "26.04", State: "adiado"})}}); !strings.Contains(text, "feche o programa") {
		t.Errorf("adiado: %q", text)
	}
}
