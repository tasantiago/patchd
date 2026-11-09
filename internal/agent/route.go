package agent

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Relé automático (docs/rele.md): o servidor manda no check-in o endereço do relé na nuvem, e o
// agente o guarda. A cada ciclo, ele tenta o servidor direto (prazo curto); sem resposta (fora
// da rede: o nome interno nem resolve), passa a discar o relé, e volta sozinho quando alcança o
// servidor de novo. Só o endereço discado muda: o TLS continua conferindo o certificado no
// nome do servidor (com a CA interna, -ca-file), então um relé falso não se passa por ele.

const (
	routeProbeTimeout = 3 * time.Second
	// routeRecheck: com a escolha feita, o agente só testa de novo depois disso (ou na
	// mudança de rede, ou depois de uma falha).
	routeRecheck = time.Minute
)

// Route decide por onde o agente fala com o servidor.
type Route struct {
	mu      sync.Mutex
	relay   string // host:porta; "" = sem relé
	via     bool   // falando pelo relé agora
	checked time.Time
	stale   bool
	// probe testa se o servidor responde direto (nos testes, um falso).
	probe func(ctx context.Context, addr string) error
	now   func() time.Time
	// idle fecha as conexões guardadas quando o caminho muda (as do caminho antigo).
	idle   func()
	logger interface{ Info(string, ...any) }
}

// NewRoute cria a rota (sem relé até o servidor mandar um). logger pode ser nil.
func NewRoute(logger interface{ Info(string, ...any) }) *Route {
	return &Route{probe: dialProbe, now: time.Now, logger: logger}
}

func dialProbe(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: routeProbeTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// SetRelay troca o endereço do relé (o que veio do check-in, ou do estado guardado). Endereço
// inválido vira "sem relé".
func (r *Route) SetRelay(addr string) bool {
	if r == nil {
		return false
	}
	if addr != "" && protocol.CheckRelayAddr(addr) != nil {
		addr = ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.relay == addr {
		return false
	}
	r.relay, r.stale = addr, true
	if addr == "" && r.via {
		r.via = false
		r.closeIdle()
	}
	return true
}

// Relay: o endereço guardado.
func (r *Route) Relay() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.relay
}

// ViaRelay diz se o agente está falando pelo relé.
func (r *Route) ViaRelay() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.via
}

// Invalidate faz o próximo ciclo testar de novo (mudança de rede, falha de conexão).
func (r *Route) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.stale = true
	r.mu.Unlock()
}

func (r *Route) closeIdle() {
	if r.idle != nil {
		r.idle()
	}
}

// primaryAddr: o host:porta do servidor, pela URL do agente. O relé só vale com HTTPS: ele
// repassa a porta só de agentes, que é TLS.
func primaryAddr(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), true
}

// Choose decide o caminho antes de um ciclo: direto se o servidor responde, senão o relé.
func (r *Route) Choose(ctx context.Context, baseURL string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	relay, recent := r.relay, !r.stale && r.now().Sub(r.checked) < routeRecheck
	r.mu.Unlock()
	primary, ok := primaryAddr(baseURL)
	if relay == "" || !ok {
		r.set(false, "")
		return
	}
	if recent {
		return
	}
	err := r.probe(ctx, primary)
	if ctx.Err() != nil {
		return
	}
	r.set(err != nil, relay)
}

func (r *Route) set(via bool, relay string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked, r.stale = r.now(), false
	if r.via == via {
		return
	}
	r.via = via
	r.closeIdle()
	if r.logger != nil {
		if via {
			r.logger.Info("rede: o servidor não responde direto; falando pelo relé", "rele", relay)
		} else {
			r.logger.Info("rede: o servidor responde direto de novo; relé fora de uso")
		}
	}
}

// Wrap devolve o transporte que disca o relé quando a rota manda. Só o endereço discado muda:
// o http.Transport continua usando o host da URL no TLS (SNI e conferência do certificado).
func (r *Route) Wrap(rt http.RoundTripper) http.RoundTripper {
	var t *http.Transport
	switch b := rt.(type) {
	case nil:
		t = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		t = b.Clone()
	default:
		return rt // transporte desconhecido (testes): sem relé
	}
	dial := t.DialContext
	if dial == nil {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		dial = d.DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		r.mu.Lock()
		via, relay := r.via, r.relay
		r.mu.Unlock()
		if via && relay != "" {
			addr = relay
		}
		return dial(ctx, network, addr)
	}
	r.idle = t.CloseIdleConnections
	return t
}
