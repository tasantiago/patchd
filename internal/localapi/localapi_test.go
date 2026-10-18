package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Atributos de um grupo comum no token: obrigatório, ligado por padrão, ligado.
const ligado = 0x7

func tokenDe(conta string, grupos ...TokenGroup) TokenInfo {
	return TokenInfo{UserSID: "S-1-5-21-1-2-3-1001", Account: conta, Groups: grupos}
}

var interativo = TokenGroup{SID: sidInteractive, Attrs: ligado}

func TestIdentityFrom(t *testing.T) {
	id, err := IdentityFrom(tokenDe(`EXEMPLO\teste`, interativo,
		TokenGroup{SID: "S-1-5-21-1-2-3-513", Attrs: ligado, Name: `EXEMPLO\Domain Users`},
		TokenGroup{SID: "S-1-5-21-1-2-3-2001", Attrs: ligado, Name: `EXEMPLO\GG-Exemplo`},
		TokenGroup{SID: "S-1-5-21-1-2-3-2001", Attrs: ligado, Name: `exemplo\gg-exemplo`}, // repetido
		TokenGroup{SID: "S-1-5-32-544", Attrs: groupDenyOnly, Name: `BUILTIN\Administradores`},
		TokenGroup{SID: "S-1-5-5-0-1234", Attrs: groupLogonID | ligado, Name: "sessão"},
		TokenGroup{SID: "S-1-16-8192", Attrs: groupIntegrity | 0x40, Name: `Mandatory Label\Medium`},
		TokenGroup{SID: "S-1-5-21-1-2-3-2002", Attrs: 0x0, Name: `EXEMPLO\GG-Desligado`},
		TokenGroup{SID: "S-1-5-21-9-9-9-2003", Attrs: ligado}, // nome não resolvido
	))
	if err != nil {
		t.Fatal(err)
	}
	quer := []string{`EXEMPLO\Domain Users`, `EXEMPLO\GG-Exemplo`}
	if fmt.Sprint(id.Groups) != fmt.Sprint(quer) || id.Account != `EXEMPLO\teste` || id.Unresolved != 1 {
		t.Errorf("identidade = %+v; quer os grupos %v e 1 sem nome", id, quer)
	}

	casos := []struct {
		nome string
		tok  TokenInfo
		erro string
	}{
		{"sem INTERATIVO (serviço, tarefa agendada, rede)", tokenDe(`EXEMPLO\teste`), "sessão interativa"},
		{"INTERATIVO só para negar", tokenDe(`EXEMPLO\teste`, TokenGroup{SID: sidInteractive, Attrs: groupDenyOnly}), "sessão interativa"},
		{"SYSTEM", TokenInfo{UserSID: sidLocalSystem, Account: `NT AUTHORITY\SYSTEM`, Groups: []TokenGroup{interativo}}, "conta de serviço"},
		{"SERVIÇO DE REDE", TokenInfo{UserSID: sidNetworkService, Account: "x", Groups: []TokenGroup{interativo}}, "conta de serviço"},
		{"conta sem nome", tokenDe("", interativo), "não foi resolvido"},
	}
	for _, c := range casos {
		if _, err := IdentityFrom(c.tok); err == nil || !strings.Contains(err.Error(), c.erro) {
			t.Errorf("%s: erro = %v, quer %q", c.nome, err, c.erro)
		}
	}
}

// backend falso: conta as chamadas e responde o combinado.
type backend struct {
	mu     sync.Mutex
	cat    atomic.Int32
	req    atomic.Int32
	user   protocol.SelfServiceUser
	resp   protocol.SelfServiceResponse
	err    error
	delay  time.Duration
	reboot *protocol.SelfServiceRebootState
	loja   *protocol.SelfServiceCatalog // Aula 18.4: a loja inteira, quando o teste precisa
}

func (b *backend) SelfServiceCatalog(ctx context.Context, u protocol.SelfServiceUser) (protocol.SelfServiceCatalog, error) {
	b.cat.Add(1)
	b.mu.Lock()
	b.user = u
	b.mu.Unlock()
	cat := protocol.SelfServiceCatalog{Items: []protocol.SelfServiceItem{{App: "7zip", Action: "em_dia"}}}
	if b.loja != nil {
		cat = *b.loja
	}
	if b.reboot != nil {
		rb := *b.reboot
		cat.Reboot = &rb
	}
	return cat, b.err
}

func (b *backend) SelfServiceRequest(ctx context.Context, r protocol.SelfServiceRequest) (protocol.SelfServiceResponse, error) {
	b.req.Add(1)
	b.mu.Lock()
	b.user = r.SelfServiceUser
	b.mu.Unlock()
	if b.delay > 0 {
		select {
		case <-time.After(b.delay):
		case <-ctx.Done():
		}
	}
	return b.resp, b.err
}

func servidor(b Backend) (*Server, *atomic.Int32, *time.Time) {
	var acordou atomic.Int32
	agora := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	s := &Server{Backend: b, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), OnCreated: func() { acordou.Add(1) }}
	s.now = func() time.Time { return agora }
	return s, &acordou, &agora
}

func TestServeLojaEPedido(t *testing.T) {
	b := &backend{resp: protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: 50, Message: "ok"}}
	s, acordou, agora := servidor(b)
	ctx := context.Background()
	id := Identity{Account: `EXEMPLO\teste`, Groups: []string{`EXEMPLO\GG-Exemplo`}}

	r := s.Serve(ctx, id, Request{Op: OpCatalog})
	if !r.OK || r.Catalog == nil || len(r.Catalog.Items) != 1 || b.user.Account != `EXEMPLO\teste` || len(b.user.Groups) != 1 {
		t.Fatalf("loja: %+v (usuário enviado %+v)", r, b.user)
	}
	r = s.Serve(ctx, Identity{Account: `EXEMPLO\semgrupo`}, Request{Op: OpCatalog})
	if !r.OK || b.user.Groups == nil {
		t.Errorf("sem grupos vai lista vazia, não null (o servidor exige o campo): %+v", b.user)
	}
	if r := s.Serve(ctx, id, Request{Op: OpWhoami}); !r.OK || r.Account != id.Account || len(r.Groups) != 1 || b.cat.Load() != 2 {
		t.Errorf("whoami responde sem ir ao servidor: %+v (lojas pedidas %d)", r, b.cat.Load())
	}

	// O app escolhe o programa e a ação; a conta e os grupos são os do token, sempre.
	for _, ruim := range []Request{{Op: OpRequest, App: "7zip", Action: "remover"}, {Op: OpRequest, App: "../x", Action: "instalar"},
		{Op: OpRequest, App: "", Action: "instalar"}, {Op: "apagar"}} {
		if r := s.Serve(ctx, id, ruim); r.OK || r.Code != CodeInvalid {
			t.Errorf("%+v: %+v", ruim, r)
		}
	}
	if b.req.Load() != 0 {
		t.Fatal("pedido malformado não vai ao servidor")
	}
	r = s.Serve(ctx, id, Request{Op: OpRequest, App: "7zip", Action: protocol.SelfServiceUpdate})
	if !r.OK || r.Result.JobID != 50 || acordou.Load() != 1 {
		t.Fatalf("pedido criado: %+v; check-in antecipado %d vez(es)", r, acordou.Load())
	}

	// Recusado ou "aguarde": não acorda o agente (não há job novo).
	b.resp = protocol.SelfServiceResponse{Status: protocol.SelfServiceRefused, Message: "não disponível"}
	if r := s.Serve(ctx, id, Request{Op: OpRequest, App: "7zip", Action: protocol.SelfServiceUpdate}); !r.OK || r.Result.Status != "recusado" || acordou.Load() != 1 {
		t.Errorf("recusado: %+v, acordou %d", r, acordou.Load())
	}

	// Limite por usuário: 5 pedidos em 10 minutos (contam os recusados também).
	for i := 0; i < RequestLimit-2; i++ {
		s.Serve(ctx, id, Request{Op: OpRequest, App: "7zip", Action: protocol.SelfServiceUpdate})
	}
	r = s.Serve(ctx, id, Request{Op: OpRequest, App: "7zip", Action: protocol.SelfServiceUpdate})
	if r.OK || r.Code != CodeLimit || !strings.Contains(r.Error, "10 minuto") || b.req.Load() != RequestLimit {
		t.Errorf("6º pedido: %+v (foram ao servidor %d)", r, b.req.Load())
	}
	// Outro usuário na mesma máquina tem o próprio limite; o mesmo, com outra grafia, não.
	if r := s.Serve(ctx, Identity{Account: `EXEMPLO\outra`}, Request{Op: OpRequest, App: "7zip", Action: "atualizar"}); r.Code == CodeLimit {
		t.Error("o limite é por usuário")
	}
	if r := s.Serve(ctx, Identity{Account: `exemplo\TESTE`}, Request{Op: OpRequest, App: "7zip", Action: "atualizar"}); r.Code != CodeLimit {
		t.Error("a conta não diferencia maiúsculas")
	}
	*agora = agora.Add(RequestWindow)
	if r := s.Serve(ctx, id, Request{Op: OpRequest, App: "7zip", Action: "atualizar"}); r.Code == CodeLimit {
		t.Error("passada a janela, o usuário volta a pedir")
	}
}

func TestServeErrosDoServidor(t *testing.T) {
	id := Identity{Account: `EXEMPLO\teste`}
	casos := []struct {
		err  error
		code string
	}{
		{&agent.TransientError{Err: errors.New("503"), Code: "selfservice_off"}, CodeOff},
		{&agent.TransientError{Err: errors.New("conexão recusada")}, CodeUnavailable},
		{agent.ErrUnauthorized, CodeServer},
		{&agent.RejectedError{Status: 400, Code: "invalid_user"}, CodeServer},
	}
	for _, c := range casos {
		s, acordou, _ := servidor(&backend{err: c.err})
		r := s.Serve(context.Background(), id, Request{Op: OpRequest, App: "7zip", Action: "instalar"})
		if r.OK || r.Code != c.code || acordou.Load() != 0 || strings.Contains(r.Error, "conexão recusada") {
			t.Errorf("%v: %+v (o detalhe técnico fica no log do agente)", c.err, r)
		}
	}
}

// conn falsa: um lado do net.Pipe, com o token combinado. Como no Windows, o token só
// pode ser lido depois de o pedido ter sido lido.
type conn struct {
	net.Conn
	tok   TokenInfo
	lido  atomic.Bool
	ident atomic.Int32
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.lido.Store(true)
	}
	return n, err
}

func (c *conn) Identify() (TokenInfo, error) {
	c.ident.Add(1)
	if !c.lido.Load() {
		return TokenInfo{}, errors.New("ERROR_CANNOT_IMPERSONATE")
	}
	return c.tok, nil
}

// chama faz o papel do app: manda a linha e lê a resposta.
func chama(t *testing.T, s *Server, tok TokenInfo, linha string) (Response, *conn) {
	t.Helper()
	app, agt := net.Pipe()
	c := &conn{Conn: agt, tok: tok}
	done := make(chan struct{})
	go func() { s.HandleConn(context.Background(), c); close(done) }()
	_ = app.SetDeadline(time.Now().Add(5 * time.Second))
	// O net.Pipe não tem buffer: quem escreve espera quem lê. O pipe do Windows tem 64 KiB;
	// aqui a escrita vai numa goroutine, para o pedido grande demais (que o agente para de
	// ler no limite) não travar o teste.
	go func() { _, _ = io.WriteString(app, linha) }()
	line, err := bufio.NewReader(app).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	app.Close()
	<-done
	var r Response
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatal(err)
	}
	return r, c
}

func TestHandleConn(t *testing.T) {
	b := &backend{resp: protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: 7}}
	s, acordou, _ := servidor(b)
	s.now = time.Now // os prazos da conexão são de verdade
	tok := tokenDe(`EXEMPLO\teste`, interativo, TokenGroup{SID: "S-1-5-21-1-2-3-2001", Attrs: ligado, Name: `EXEMPLO\GG-Exemplo`})

	r, _ := chama(t, s, tok, `{"op":"request","app":"7zip","action":"atualizar"}`+"\n")
	if !r.OK || r.Result.JobID != 7 || r.Account != `EXEMPLO\teste` || acordou.Load() != 1 {
		t.Fatalf("pedido pelo pipe: %+v", r)
	}
	if b.user.Account != `EXEMPLO\teste` || fmt.Sprint(b.user.Groups) != `[EXEMPLO\GG-Exemplo]` {
		t.Errorf("o servidor recebe a conta e os grupos do token: %+v", b.user)
	}

	// O app não escolhe quem é: campos a mais (conta, grupos) são recusados.
	r, c := chama(t, s, tok, `{"op":"request","app":"7zip","action":"atualizar","account":"EXEMPLO\\chefe"}`+"\n")
	if r.OK || r.Code != CodeInvalid || c.ident.Load() != 0 {
		t.Errorf("campo account: %+v", r)
	}
	r, _ = chama(t, s, tok, strings.Repeat("x", MaxRequestSize+10)+"\n")
	if r.OK || r.Code != CodeInvalid || !strings.Contains(r.Error, "grande") {
		t.Errorf("pedido grande: %+v", r)
	}
	// Sem a sessão interativa (um serviço, uma tarefa agendada): recusado.
	r, _ = chama(t, s, tokenDe(`EXEMPLO\svc`), `{"op":"catalog"}`+"\n")
	if r.OK || r.Code != CodeIdentity {
		t.Errorf("sem INTERATIVO: %+v", r)
	}
	if b.req.Load() != 1 || b.cat.Load() != 0 {
		t.Errorf("só o primeiro pedido chegou ao servidor: pedidos %d, lojas %d", b.req.Load(), b.cat.Load())
	}
}

func TestHandleConnOcupado(t *testing.T) {
	b := &backend{resp: protocol.SelfServiceResponse{Status: protocol.SelfServiceWait}, delay: 300 * time.Millisecond}
	s, _, _ := servidor(b)
	s.now = time.Now
	tok := tokenDe(`EXEMPLO\teste`, interativo)
	res := make(chan Response, MaxConns+1)
	for i := 0; i < MaxConns+1; i++ {
		go func(i int) {
			tk := tok
			tk.Account = fmt.Sprintf(`EXEMPLO\u%d`, i)
			r, _ := chama(t, s, tk, `{"op":"request","app":"7zip","action":"instalar"}`+"\n")
			res <- r
		}(i)
		time.Sleep(20 * time.Millisecond)
	}
	ocupado := 0
	for i := 0; i < MaxConns+1; i++ {
		if r := <-res; r.Code == CodeBusy {
			ocupado++
		}
	}
	if ocupado != 1 {
		t.Errorf("com %d conexões ao mesmo tempo, a %dª ouve \"ocupado\": %d", MaxConns, MaxConns+1, ocupado)
	}
}

func TestExchange(t *testing.T) {
	app, agt := net.Pipe()
	go func() {
		req, _ := readRequest(agt)
		b, _ := json.Marshal(Response{OK: true, Account: req.App})
		_, _ = agt.Write(append(b, '\n'))
		agt.Close()
	}()
	r, err := exchange(app, Request{Op: OpRequest, App: "7zip", Action: "atualizar"})
	if err != nil || !r.OK || r.Account != "7zip" {
		t.Fatalf("%+v %v", r, err)
	}
}

// Aula 17.6: o reinício pelo canal — sem programa, com a hora, e o pendente lido na hora.
func TestServeReinicio(t *testing.T) {
	sim, nao := true, false
	b := &backend{resp: protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: 80}}
	s, acordou, _ := servidor(b)
	id := Identity{Account: `EXEMPLO\teste`}
	ctx := context.Background()
	as18 := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	for _, ruim := range []Request{{Op: OpRequest, App: "7zip", Action: "reiniciar"}, {Op: OpRequest, App: "7zip", Action: "atualizar", At: &as18}} {
		if r := s.Serve(ctx, id, ruim); r.Code != CodeInvalid {
			t.Errorf("%+v: %+v", ruim, r)
		}
	}
	var got protocol.SelfServiceRequest
	s.Backend = backendFunc(func(r protocol.SelfServiceRequest) { got = r }, b)
	if r := s.Serve(ctx, id, Request{Op: OpRequest, Action: "reiniciar", At: &as18}); !r.OK || r.Result.JobID != 80 || acordou.Load() != 1 {
		t.Fatalf("reiniciar: %+v", r)
	}
	if got.Action != "reiniciar" || got.RebootAt == nil || !got.RebootAt.Equal(as18) || got.App != "" {
		t.Errorf("o servidor recebe a hora: %+v", got)
	}

	// A loja do servidor diz "pendente" (busca de ontem); o agente lê agora e corrige.
	b.reboot = &protocol.SelfServiceRebootState{Pending: &sim, Allowed: true}
	s.Backend = b
	s.RebootStatus = func(context.Context) (protocol.RebootStatus, error) { return protocol.RebootStatus{Pending: &nao}, nil }
	if r := s.Serve(ctx, id, Request{Op: OpCatalog}); r.Catalog.Reboot == nil || *r.Catalog.Reboot.Pending {
		t.Errorf("pendente de agora: %+v", r.Catalog.Reboot)
	}
	// Sem resposta do agente (nil): fica o do servidor.
	s.RebootStatus = func(context.Context) (protocol.RebootStatus, error) { return protocol.RebootStatus{}, nil }
	if r := s.Serve(ctx, id, Request{Op: OpCatalog}); !*r.Catalog.Reboot.Pending {
		t.Errorf("sem leitura, o do servidor: %+v", r.Catalog.Reboot)
	}
}

// spy embrulha o backend e guarda o pedido recebido.
type spy struct {
	*backend
	see func(protocol.SelfServiceRequest)
}

func (s spy) SelfServiceRequest(ctx context.Context, r protocol.SelfServiceRequest) (protocol.SelfServiceResponse, error) {
	s.see(r)
	return s.backend.SelfServiceRequest(ctx, r)
}

func backendFunc(see func(protocol.SelfServiceRequest), b *backend) Backend { return spy{b, see} }

// Aula 18.4: a loja com um job do usuário ainda não entregue (a TI aprovou) acorda o agente.
func TestLojaComJobPendenteAcorda(t *testing.T) {
	b := &backend{}
	s, acordou, agora := servidor(b)
	b.loja = &protocol.SelfServiceCatalog{Requests: []protocol.SelfServiceJob{{ID: 90, State: "pendente", CreatedAt: *agora}}}
	s.Serve(context.Background(), Identity{Account: `EXEMPLO\ana`}, Request{Op: OpCatalog})
	if acordou.Load() != 1 {
		t.Errorf("job pendente: acordou %d", acordou.Load())
	}
	b.loja.Requests = []protocol.SelfServiceJob{{ID: 90, State: "entregue", CreatedAt: *agora}, {Request: 3, State: "aguardando aprovação", CreatedAt: *agora},
		{ID: 80, State: "pendente", CreatedAt: agora.Add(-48 * time.Hour)}}
	s.Serve(context.Background(), Identity{Account: `EXEMPLO\ana`}, Request{Op: OpCatalog})
	if acordou.Load() != 1 {
		t.Errorf("entregue, aguardando a TI ou velho não acordam: %d", acordou.Load())
	}
}
