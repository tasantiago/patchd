package userapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
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
	if !strings.Contains(b, `<span class="cont" title="atualizações disponíveis">1</span>`) {
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

// Aula 17.6: a aba Reinício — escolher a hora, acompanhar, e o aviso no ícone.
func TestAbaReinicio(t *testing.T) {
	sim, nao := true, false
	loja := lojaDeTeste(0)
	loja.Catalog.Reboot = &protocol.SelfServiceRebootState{Pending: &sim, Allowed: true}
	c := &canal{loja: loja, pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 90, Message: "Pedido aceito: o computador reinicia no horário escolhido."}}}
	a := appDeTeste(c) // 17/10 09:30 UTC
	a.Loc = time.UTC

	b := get(a, a.base()+"?aba=reinicio").Body.String()
	for _, quer := range []string{`class="cont alerta"`, `value="agora" checked`, "Daqui a 1 hora (às 10:30)", "Na hora do almoço (12:00)",
		"No fim do expediente (18:00)", "Daqui a 4 horas (às 13:30)", `name="action" value="reiniciar"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("aba reinício sem %q:\n%s", quer, b)
		}
	}
	if strings.Index(b, "Na hora do almoço") > strings.Index(b, "Daqui a 4 horas") {
		t.Error("as opções vão em ordem de hora")
	}

	// Pedido para as 18:00: vai a hora; volta para a própria aba.
	as18 := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	w := post(a, a.base()+"pedir", url.Values{"action": {"reiniciar"}, "quando": {fmt.Sprint(as18.Unix())}}, "http://127.0.0.1:5555")
	if w.Header().Get("Location") != a.base()+"?aba=reinicio" || len(c.pedidos) != 1 || c.pedidos[0].At == nil || !c.pedidos[0].At.Equal(as18) || c.pedidos[0].App != "" {
		t.Fatalf("pedido: %v %+v", w.Header(), c.pedidos)
	}
	post(a, a.base()+"pedir", url.Values{"action": {"reiniciar"}, "quando": {"agora"}}, "http://127.0.0.1:5555")
	if c.pedidos[1].At != nil {
		t.Error("agora vai sem hora")
	}
	// Hora fora da lista (amanhã, ou no passado): nem chega ao agente.
	for _, q := range []string{fmt.Sprint(as18.Add(24 * time.Hour).Unix()), fmt.Sprint(as18.Add(-10 * time.Hour).Unix()), "x"} {
		post(a, a.base()+"pedir", url.Values{"action": {"reiniciar"}, "quando": {q}}, "http://127.0.0.1:5555")
	}
	if len(c.pedidos) != 2 {
		t.Errorf("horas inválidas chegaram ao agente: %+v", c.pedidos)
	}

	// Já agendado: mostra a hora, sem formulário nem alerta.
	c.mu.Lock()
	c.loja.Catalog.Reboot = &protocol.SelfServiceRebootState{Pending: &sim, Allowed: true, JobID: 90, At: &as18}
	c.mu.Unlock()
	b = get(a, a.base()+"?aba=reinicio").Body.String()
	if !strings.Contains(b, "Reinício agendado para as <b>18:00</b> (pedido nº 90)") || strings.Contains(b, "cont alerta") || strings.Contains(b, `name="quando"`) {
		t.Errorf("agendado:\n%s", b)
	}
	// Sem pendência; em servidor; agente antigo (sem o campo): a aba some.
	c.mu.Lock()
	c.loja.Catalog.Reboot = &protocol.SelfServiceRebootState{Pending: &nao, Allowed: true}
	c.mu.Unlock()
	if b := get(a, a.base()+"?aba=reinicio").Body.String(); !strings.Contains(b, "Nenhum reinício pendente") {
		t.Errorf("sem pendência:\n%s", b)
	}
	c.mu.Lock()
	c.loja.Catalog.Reboot = &protocol.SelfServiceRebootState{Pending: &sim}
	c.mu.Unlock()
	if b := get(a, a.base()+"?aba=reinicio").Body.String(); !strings.Contains(b, "equipe de TI") || strings.Contains(b, `name="quando"`) {
		t.Errorf("servidor:\n%s", b)
	}
	c.mu.Lock()
	c.loja.Catalog.Reboot = nil
	c.mu.Unlock()
	if b := get(a, a.base()+"?aba=reinicio").Body.String(); strings.Contains(b, "Reinício") || !strings.Contains(b, "Atualizações para você") {
		t.Errorf("sem o campo, sem a aba:\n%s", b)
	}

	// O aviso no ícone: pendente sem hora escolhida; o clique abre a aba Reinício.
	n := Notice{Reboot: true}
	if title, _ := noticeText(n); title != "Reinício pendente" || n.Tab() != "reinicio" {
		t.Errorf("aviso: %q %q", title, n.Tab())
	}
	if (Notice{Done: []string{"x"}, Reboot: true}).Tab() != "pedidos" || a.TabURL("reinicio") != a.URL()+"?aba=reinicio" || a.TabURL("") != a.URL() {
		t.Error("abas do aviso")
	}
}

func TestAvisoDeReinicio(t *testing.T) {
	sim := true
	loja := lojaDeTeste(0)
	loja.Catalog.Items = nil
	loja.Catalog.Reboot = &protocol.SelfServiceRebootState{Pending: &sim, Allowed: true}
	a := appDeTeste(&canal{loja: loja})
	out := make(chan Notice, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Watch(ctx, time.Millisecond, time.Hour, out)
	if n := <-out; !n.Reboot {
		t.Fatalf("aviso: %+v", n)
	}
}

// Aula 18.4: o que passa pela TI — o botão, o pedido esperando e a resposta no ícone.
func TestPedidosComAprovacaoDaTI(t *testing.T) {
	loja := lojaDeTeste(0)
	loja.Catalog.Items[0].Approval = true                                   // 7-Zip: atualizar com aprovação
	loja.Catalog.Items[2].Approval, loja.Catalog.Items[2].Pending = true, 3 // VS Code: já com a TI
	loja.Catalog.Items[2].Target = "1.105.1"
	loja.Catalog.Requests = []protocol.SelfServiceJob{
		{Request: 3, App: "vscode", Name: "Visual Studio Code", Version: "1.105.1", State: "aguardando aprovação", CreatedAt: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)},
		{Request: 2, App: "7zip", Name: "7-Zip", Version: "26.04", State: "recusado pela TI", Detail: "motivo: use a versão da imagem", CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)},
	}
	c := &canal{loja: loja, pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "aprovacao", RequestID: 4, Message: "Pedido enviado para a aprovação da TI (nº 4)."}}}
	a := appDeTeste(c)
	a.Loc = time.UTC

	b := get(a, a.base()).Body.String()
	if !strings.Contains(b, "com aprovação da TI") || !strings.Contains(b, ">Pedir à TI</button>") {
		t.Errorf("atualizar com aprovação:\n%s", b)
	}
	b = get(a, a.base()+"?aba=loja").Body.String()
	if !strings.Contains(b, "Com a TI (pedido nº 3)") || strings.Contains(b, "confirmar=vscode") {
		t.Errorf("loja com o pedido esperando:\n%s", b)
	}
	b = get(a, a.base()+"?aba=pedidos").Body.String()
	for _, quer := range []string{"pedido à TI nº 3", ">Com a TI<", ">Recusado pela TI<", "motivo: use a versão da imagem"} {
		if !strings.Contains(b, quer) {
			t.Errorf("meus pedidos sem %q", quer)
		}
	}
	w := post(a, a.base()+"pedir", url.Values{"app": {"7zip"}, "action": {"atualizar"}}, "http://127.0.0.1:5555")
	if w.Header().Get("Location") != a.base()+"?aba=pedidos" {
		t.Errorf("pedido à TI vai para Meus pedidos: %v", w.Header())
	}
	if b := get(a, a.base()+"?aba=pedidos").Body.String(); !strings.Contains(b, `class="aviso sucesso"`) {
		t.Error("aviso do pedido à TI")
	}
}

func TestAvisoDaDecisaoDaTI(t *testing.T) {
	loja := lojaDeTeste(0)
	loja.Catalog.Items = nil
	espera := func(id int64, nome string) protocol.SelfServiceJob {
		return protocol.SelfServiceJob{Request: id, Name: nome, Version: "1.0", State: "aguardando aprovação"}
	}
	loja.Catalog.Requests = []protocol.SelfServiceJob{espera(3, "Visual Studio Code"), espera(4, "LibreOffice")}
	c := &canal{loja: loja}
	a := appDeTeste(c)
	a.approvalEvery = 10 * time.Millisecond
	out := make(chan Notice, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Watch(ctx, time.Millisecond, time.Hour, out)
	time.Sleep(30 * time.Millisecond)
	c.mu.Lock() // o 3 foi recusado; o 4 virou job (aprovado)
	c.loja.Catalog.Requests = []protocol.SelfServiceJob{{Request: 3, Name: "Visual Studio Code", Version: "1.0", State: "recusado pela TI", Detail: "motivo: fora do setor"},
		{ID: 90, Name: "LibreOffice", Version: "1.0", State: "pendente"}}
	c.mu.Unlock()
	select {
	case n := <-out:
		title, text := noticeText(n)
		if title != "Pedidos concluídos" || !strings.Contains(text, "Visual Studio Code 1.0: recusado pela TI (fora do setor)") ||
			!strings.Contains(text, "LibreOffice 1.0: aprovado pela TI") {
			t.Errorf("aviso: %q %q", title, text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a decisão da TI não virou aviso")
	}
	if title, _ := noticeText(Notice{Done: []string{"7-Zip 26.04: recusado pela TI"}}); title != "Resposta da TI" {
		t.Errorf("título de uma decisão: %q", title)
	}
}

// Aula 19.1b: a Central no layout do painel. Barra lateral com o logotipo do painel (que o
// agente entregou e a página conferiu), o usuário e as seções; barra superior com a consulta
// e a versão do agente.
func TestLayoutDaCentral(t *testing.T) {
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewGray(image.Rect(0, 0, 16, 16)))
	logo := img.Bytes()
	sum := sha256.Sum256(logo)
	sha := hex.EncodeToString(sum[:])
	var pediuLogo int
	loja := localapi.Response{OK: true, Account: `EXEMPLO\joao.silva`, Catalog: &protocol.SelfServiceCatalog{Brand: "patchd · TJ", Logo: sha,
		Items:  []protocol.SelfServiceItem{{App: "7zip", Name: "7-Zip", Installed: "26.01", Target: "26.04", Action: "atualizar"}},
		Reboot: &protocol.SelfServiceRebootState{Allowed: true}}}
	entrega := logo
	a := New(func(_ context.Context, r localapi.Request) (localapi.Response, error) {
		if r.Op == localapi.OpLogo {
			pediuLogo++
			return localapi.Response{OK: true, Logo: entrega, LogoType: "image/png"}, nil
		}
		return loja, nil
	})
	a.Addr, a.Version = "127.0.0.1:5555", "v0.17.6"
	a.Now = func() time.Time { return time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC) }

	b := get(a, a.base()).Body.String()
	src := a.base() + "logo?v=" + sha[:16]
	for _, tr := range []string{
		`<aside class="lado"`, `<input type="checkbox" id="menu" class="menu-chk"`, `<img src="` + src + `"`,
		`Central de software<small>patchd · TJ</small>`, `<span class="avatar" aria-hidden="true">JS</span>`,
		`<strong>joao.silva</strong><small>EXEMPLO · este computador</small>`,
		`<p class="grupo"><span class="rot">Software</span></p>`, `<p class="grupo"><span class="rot">Computador</span></p>`,
		`title="Atualizações" class="ativo" aria-current="page">`, `<span class="cont" title="atualizações disponíveis">1</span>`,
		`<span class="titulo">Atualizações</span>`, `Agente <b>v0.17.6</b>`, `Consultado às 19/10 09:30`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	if strings.Contains(b, "<script") {
		t.Error("sem JavaScript")
	}
	get(a, a.base()+"?aba=loja")
	if pediuLogo != 1 {
		t.Errorf("o logotipo é pedido uma vez só: %d", pediuLogo)
	}
	w := get(a, src)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), logo) ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "img-src 'self' data:") {
		t.Errorf("servir o logotipo: %d %v", w.Code, w.Header())
	}
	if w := get(a, "/s/outro/logo"); w.Code != 404 {
		t.Error("sem o segredo, nada")
	}

	// O agente entregou outra coisa (ou nada): fica o ícone padrão.
	b2 := New(func(_ context.Context, r localapi.Request) (localapi.Response, error) {
		if r.Op == localapi.OpLogo {
			return localapi.Response{OK: true, Logo: []byte("GIF89a"), LogoType: "image/png"}, nil
		}
		return loja, nil
	})
	b2.Addr = "127.0.0.1:5555"
	if p := get(b2, b2.base()).Body.String(); strings.Contains(p, "<img") || !strings.Contains(p, `<span class="escudo">`) {
		t.Error("logotipo que não confere não aparece")
	}
	if w := get(b2, b2.base()+"logo"); w.Code != 404 {
		t.Error("sem logotipo guardado, 404")
	}
}

// Aula 20.6: a Central sem CSS inline; o estilo vem de estilo.css, sob o mesmo segredo.
func TestEstiloDaCentral(t *testing.T) {
	a := appDeTeste(&canal{loja: lojaDeTeste(0)})
	w := get(a, a.base())
	b := strings.ToLower(w.Body.String())
	if strings.Contains(b, "<style") || strings.Contains(b, " style=") || !strings.Contains(b, `href="`+strings.ToLower(a.base())+`estilo.css"`) {
		t.Error("página com CSS inline ou sem o link para o estilo")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP: %q", csp)
	}
	css := get(a, a.base()+"estilo.css")
	if css.Code != 200 || css.Header().Get("Content-Type") != "text/css; charset=utf-8" || !strings.Contains(css.Body.String(), ".barra-topo") {
		t.Errorf("estilo.css: %d %q", css.Code, css.Header().Get("Content-Type"))
	}
	if w := get(a, "/s/segredo-errado/estilo.css"); w.Code != 404 {
		t.Errorf("estilo.css sem o segredo: %d", w.Code)
	}
}

// Aula 20.10: o nome da Central que vem na loja vale na página; SetTitle só aceita nome válido.
func TestNomeDaCentralNaPagina(t *testing.T) {
	loja := localapi.Response{OK: true, Account: `EXEMPLO\joao.silva`, Catalog: &protocol.SelfServiceCatalog{AppName: "Atualizações da TI"}}
	a := New(func(_ context.Context, r localapi.Request) (localapi.Response, error) { return loja, nil })
	a.Addr = "127.0.0.1:5555"
	b := get(a, a.base()).Body.String()
	if !strings.Contains(b, "<title>Atualizações da TI") && !strings.Contains(b, ">Atualizações da TI<") {
		t.Errorf("o nome do painel não apareceu:\n%s", b)
	}
	antes := Title
	t.Cleanup(func() { Title = antes })
	SetTitle("a\x00b")
	if Title != antes {
		t.Error("nome inválido aceito")
	}
	SetTitle("TI Responde")
	if Title != "TI Responde" {
		t.Errorf("Title: %q", Title)
	}
}
