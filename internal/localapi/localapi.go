// Package localapi é o canal local do autoatendimento (Aula 17.3): o app do usuário (17.4),
// ou o "patchd-agent selfservice" na linha de comando, fala com o agente da própria máquina
// por um named pipe, e o agente fala com o servidor com a credencial da máquina.
//
// Quem pediu não é algo que o app diz: o agente lê a conta e os grupos do token do Windows
// de quem abriu o pipe. O app só escolhe o que pedir; um app adulterado consegue, no máximo,
// pedir em nome do próprio usuário o que a loja dele já oferece.
package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/protocol"
)

// PipeName é o nome do canal. Um nome fixo: o app não precisa de configuração.
const PipeName = `\\.\pipe\patchd-autoatendimento`

// Operações do canal.
const (
	OpWhoami  = "whoami"  // quem o agente vê: conta e grupos do token
	OpCatalog = "catalog" // a loja do usuário nesta máquina
	OpRequest = "request" // pedir atualizar ou instalar
)

// Códigos de erro do canal (Response.Code).
const (
	CodeInvalid     = "invalido"     // pedido malformado
	CodeIdentity    = "identidade"   // não foi possível saber quem chamou, ou não é usuário interativo
	CodeLimit       = "limite"       // pedidos demais deste usuário
	CodeBusy        = "ocupado"      // conexões demais ao mesmo tempo
	CodeOff         = "desligado"    // o autoatendimento está desligado no servidor
	CodeUnavailable = "indisponivel" // o servidor não respondeu
	CodeServer      = "servidor"     // o servidor recusou (credencial, versão etc.)
)

// MaxRequestSize: o pedido é uma linha de JSON pequena; mais que isso é recusado.
const MaxRequestSize = 4 << 10

// Request é o que o app manda: uma linha de JSON por conexão.
type Request struct {
	Op     string `json:"op"`
	App    string `json:"app,omitempty"`
	Action string `json:"action,omitempty"`
	// At (Aula 17.6): no "reiniciar", a hora escolhida; vazio: agora.
	At *time.Time `json:"at,omitempty"`
}

// Response é a resposta do agente: uma linha de JSON, e a conexão termina.
type Response struct {
	OK      bool                          `json:"ok"`
	Code    string                        `json:"code,omitempty"`
	Error   string                        `json:"error,omitempty"`
	Account string                        `json:"account,omitempty"`
	Groups  []string                      `json:"groups,omitempty"` // só no whoami
	Catalog *protocol.SelfServiceCatalog  `json:"catalog,omitempty"`
	Result  *protocol.SelfServiceResponse `json:"result,omitempty"`
}

// Identity é quem abriu o pipe, como o token do Windows diz.
type Identity struct {
	Account    string   // DOMINIO\usuario
	Groups     []string // DOMINIO\grupo, sem os desligados e os "só para negar"
	Unresolved int      // grupos cujo nome não foi resolvido (ficaram de fora)
}

// SIDs que decidem quem pode usar o canal.
const (
	sidInteractive    = "S-1-5-4" // INTERATIVO: logon no console ou por área de trabalho remota
	sidLocalSystem    = "S-1-5-18"
	sidLocalService   = "S-1-5-19"
	sidNetworkService = "S-1-5-20"
)

// Atributos dos grupos no token (winnt.h).
const (
	groupEnabled   = 0x00000004
	groupDenyOnly  = 0x00000010
	groupIntegrity = 0x00000020
	groupLogonID   = 0xC0000000
)

// TokenGroup é um grupo do token: o SID, os atributos e o nome (vazio se não resolvido).
type TokenGroup struct {
	SID   string
	Attrs uint32
	Name  string
}

// TokenInfo é o que o lado do Windows leu do token, já com os nomes.
type TokenInfo struct {
	UserSID string
	Account string // DOMINIO\usuario; vazio se o nome não foi resolvido
	Groups  []TokenGroup
}

// KeepGroup diz se o grupo conta para a loja: ligado, e não "só para negar" (o grupo
// Administradores de quem está com o UAC fica assim), nem o SID da sessão, nem o nível de
// integridade. O lado do Windows só resolve o nome dos que passam aqui.
func KeepGroup(attrs uint32) bool {
	return attrs&groupEnabled != 0 && attrs&groupDenyOnly == 0 && attrs&groupIntegrity == 0 && attrs&groupLogonID != groupLogonID
}

// IdentityFrom decide se quem chamou pode usar o canal e monta a identidade.
func IdentityFrom(t TokenInfo) (Identity, error) {
	switch t.UserSID {
	case sidLocalSystem, sidLocalService, sidNetworkService:
		return Identity{}, errors.New("conta de serviço: o autoatendimento é para o usuário da sessão")
	}
	interactive := slices.ContainsFunc(t.Groups, func(g TokenGroup) bool {
		return g.SID == sidInteractive && KeepGroup(g.Attrs)
	})
	if !interactive {
		return Identity{}, errors.New("não é uma sessão interativa (console ou área de trabalho remota)")
	}
	if t.Account == "" {
		return Identity{}, errors.New("o nome da conta não foi resolvido")
	}
	id := Identity{Account: t.Account}
	seen := map[string]bool{}
	for _, g := range t.Groups {
		if !KeepGroup(g.Attrs) || g.SID == sidInteractive { // INTERATIVO todos têm: não conta
			continue
		}
		if g.Name == "" {
			id.Unresolved++
			continue
		}
		k := strings.ToLower(g.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		id.Groups = append(id.Groups, g.Name)
	}
	slices.SortFunc(id.Groups, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	if len(id.Groups) > protocol.MaxSelfServiceGroups {
		id.Groups = id.Groups[:protocol.MaxSelfServiceGroups]
	}
	return id, nil
}

// Conn é uma conexão do pipe: lê e escreve com prazo, e diz quem está do outro lado.
// Identify só funciona depois de o pedido ter sido lido (exigência do Windows).
type Conn interface {
	io.Reader
	io.Writer
	SetDeadline(t time.Time) error
	Identify() (TokenInfo, error)
	Close() error
}

// Backend é o servidor, pela credencial da máquina (o *agent.Client).
type Backend interface {
	SelfServiceCatalog(ctx context.Context, u protocol.SelfServiceUser) (protocol.SelfServiceCatalog, error)
	SelfServiceRequest(ctx context.Context, r protocol.SelfServiceRequest) (protocol.SelfServiceResponse, error)
}

// Limites do canal, por usuário (a conta, sem diferenciar maiúsculas).
const (
	RequestLimit  = 5 // pedidos (atualizar, instalar)...
	RequestWindow = 10 * time.Minute
	QueryLimit    = 60 // consultas (loja, whoami)...
	QueryWindow   = 10 * time.Minute
	MaxConns      = 4 // conexões atendidas ao mesmo tempo, de todos os usuários

	readTimeout    = 10 * time.Second
	backendTimeout = time.Minute
	writeTimeout   = 10 * time.Second
	drainTimeout   = 2 * time.Second

	rebootStatusTimeout = 20 * time.Second
)

// Server atende o canal. OnCreated é chamado quando um pedido vira job: o agente faz o
// check-in na hora em vez de esperar a próxima hora.
type Server struct {
	Backend   Backend
	Logger    *slog.Logger
	OnCreated func()
	// RebootStatus (Aula 17.6): o reinício pendente lido na hora; a loja do servidor traz o
	// da última busca, que pode ter um dia. nil: fica o do servidor.
	RebootStatus func(ctx context.Context) (protocol.RebootStatus, error)

	now  func() time.Time
	once sync.Once
	sem  chan struct{}
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (s *Server) init() {
	s.once.Do(func() {
		if s.now == nil {
			s.now = time.Now
		}
		s.sem = make(chan struct{}, MaxConns)
		s.hits = map[string][]time.Time{}
	})
}

// HandleConn lê um pedido, identifica quem chamou, atende e responde. Fecha a conexão.
func (s *Server) HandleConn(ctx context.Context, c Conn) {
	s.init()
	defer c.Close()

	_ = c.SetDeadline(s.now().Add(readTimeout))
	req, err := readRequest(c)
	var resp Response
	switch {
	case err != nil:
		resp = fail(CodeInvalid, "pedido ilegível: "+err.Error())
	default:
		select {
		case s.sem <- struct{}{}:
			resp = s.identified(ctx, c, req)
			<-s.sem
		default:
			resp = fail(CodeBusy, "o agente está atendendo outros pedidos; tente de novo em instantes")
		}
	}

	_ = c.SetDeadline(s.now().Add(writeTimeout))
	b, _ := json.Marshal(resp)
	if _, err := c.Write(append(b, '\n')); err != nil {
		s.Logger.Debug("autoatendimento: resposta não entregue", "error", err)
		return
	}
	// Espera o app fechar a ponta dele: fechar antes pode descartar a resposta que ainda
	// está no buffer do pipe.
	_ = c.SetDeadline(s.now().Add(drainTimeout))
	_, _ = io.Copy(io.Discard, io.LimitReader(c, MaxRequestSize))
}

func (s *Server) identified(ctx context.Context, c Conn, req Request) Response {
	tok, err := c.Identify()
	if err != nil {
		s.Logger.Warn("autoatendimento: quem chamou não foi identificado", "error", err)
		return fail(CodeIdentity, "não foi possível identificar o usuário")
	}
	id, err := IdentityFrom(tok)
	if err != nil {
		s.Logger.Warn("autoatendimento: chamada recusada", "motivo", err.Error())
		return fail(CodeIdentity, err.Error())
	}
	if id.Unresolved > 0 {
		s.Logger.Debug("autoatendimento: grupos sem nome resolvido ficaram de fora", "quantos", id.Unresolved)
	}
	ctx, cancel := context.WithTimeout(ctx, backendTimeout)
	defer cancel()
	return s.Serve(ctx, id, req)
}

// Serve atende um pedido de quem já foi identificado.
func (s *Server) Serve(ctx context.Context, id Identity, req Request) Response {
	s.init()
	u := protocol.SelfServiceUser{Account: id.Account, Groups: id.Groups}
	if u.Groups == nil {
		u.Groups = []string{}
	}
	switch req.Op {
	case OpWhoami, OpCatalog:
		if wait, ok := s.allow("q:"+id.Account, QueryLimit, QueryWindow); !ok {
			return limited(wait)
		}
		if req.Op == OpWhoami {
			return Response{OK: true, Account: id.Account, Groups: u.Groups}
		}
		cat, err := s.Backend.SelfServiceCatalog(ctx, u)
		if err != nil {
			return s.backendError("loja", err)
		}
		if cat.Reboot != nil && s.RebootStatus != nil {
			rctx, cancel := context.WithTimeout(ctx, rebootStatusTimeout)
			st, err := s.RebootStatus(rctx)
			cancel()
			if err == nil && st.Pending != nil {
				cat.Reboot.Pending = st.Pending
			}
		}
		return Response{OK: true, Account: id.Account, Catalog: &cat}
	case OpRequest:
		if err := checkRequest(req); err != nil {
			return fail(CodeInvalid, err.Error())
		}
		if wait, ok := s.allow("r:"+id.Account, RequestLimit, RequestWindow); !ok {
			s.Logger.Warn("autoatendimento: limite de pedidos", "account", id.Account, "app", req.App)
			return limited(wait)
		}
		res, err := s.Backend.SelfServiceRequest(ctx, protocol.SelfServiceRequest{SelfServiceUser: u, App: req.App, Action: req.Action, RebootAt: req.At})
		if err != nil {
			return s.backendError("pedido", err)
		}
		s.Logger.Info("autoatendimento: pedido", "account", id.Account, "app", req.App, "acao", req.Action,
			"status", res.Status, "job_id", res.JobID)
		if res.Status == protocol.SelfServiceCreated && s.OnCreated != nil {
			s.OnCreated()
		}
		return Response{OK: true, Account: id.Account, Result: &res}
	default:
		return fail(CodeInvalid, fmt.Sprintf("operação desconhecida %q", req.Op))
	}
}

var appID = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)

func checkRequest(r Request) error {
	switch r.Action {
	case protocol.SelfServiceReboot: // Aula 17.6: o reinício é do computador, sem programa
		if r.App != "" {
			return errors.New("o reiniciar não leva programa")
		}
		return nil
	case protocol.SelfServiceUpdate, protocol.SelfServiceInstall:
		if r.At != nil {
			return errors.New("hora só no reiniciar")
		}
	default:
		return fmt.Errorf("ação inválida: use %s, %s ou %s", protocol.SelfServiceUpdate, protocol.SelfServiceInstall, protocol.SelfServiceReboot)
	}
	if !appID.MatchString(r.App) {
		return errors.New("programa inválido")
	}
	return nil
}

// allow conta uma chamada na janela do usuário; ok=false com a espera até a próxima vaga.
func (s *Server) allow(key string, limit int, window time.Duration) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key = strings.ToLower(key)
	now := s.now()
	keep := func(ts []time.Time) []time.Time {
		return slices.DeleteFunc(ts, func(t time.Time) bool { return now.Sub(t) >= window })
	}
	if len(s.hits) > 1000 { // muitos usuários: limpa os que já saíram da janela
		for k, ts := range s.hits {
			if ts = keep(ts); len(ts) == 0 {
				delete(s.hits, k)
			} else {
				s.hits[k] = ts
			}
		}
	}
	ts := keep(s.hits[key])
	if len(ts) >= limit {
		s.hits[key] = ts
		return ts[0].Add(window).Sub(now), false
	}
	s.hits[key] = append(ts, now)
	return 0, true
}

func limited(wait time.Duration) Response {
	min := int((wait + time.Minute - 1) / time.Minute)
	return fail(CodeLimit, fmt.Sprintf("pedidos demais em pouco tempo; tente de novo em %d minuto(s)", min))
}

// backendError traduz a falha com o servidor para o usuário; o detalhe fica no log.
func (s *Server) backendError(what string, err error) Response {
	var te *agent.TransientError
	var re *agent.RejectedError
	switch {
	case errors.As(err, &te) && te.Code == "selfservice_off":
		return fail(CodeOff, "o autoatendimento está desligado no servidor")
	case errors.Is(err, agent.ErrUnauthorized):
		s.Logger.Error("autoatendimento: o servidor recusou a credencial da máquina", "op", what)
		return fail(CodeServer, "o servidor não reconheceu esta máquina; avise o suporte")
	case errors.As(err, &re):
		s.Logger.Warn("autoatendimento: o servidor recusou", "op", what, "error", err)
		return fail(CodeServer, "o servidor recusou o pedido ("+re.Code+")")
	default:
		s.Logger.Warn("autoatendimento: servidor indisponível", "op", what, "error", err)
		return fail(CodeUnavailable, "o servidor não respondeu; tente de novo em alguns minutos")
	}
}

func fail(code, msg string) Response { return Response{Code: code, Error: msg} }

// readRequest lê uma linha de JSON, até MaxRequestSize.
func readRequest(r io.Reader) (Request, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, MaxRequestSize+1), MaxRequestSize+1)
	line, err := br.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > MaxRequestSize {
		return Request{}, errors.New("grande demais")
	}
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return Request{}, err
	}
	var req Request
	dec := json.NewDecoder(strings.NewReader(string(line)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, errors.New("JSON inválido")
	}
	return req, nil
}
