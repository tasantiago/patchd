package agent

import (
	"context"
	"errors"
	"net"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O agente é o caminho do autoatendimento até o servidor (Aula 17.3): o canal local
// identifica o usuário e chama estes métodos, de outra goroutine, enquanto o laço de
// check-in segue. Cada chamada usa uma cópia do cliente com o endereço de agora.

func (a *Agent) client() *Client {
	a.clientMu.RLock()
	c := *a.Client
	a.clientMu.RUnlock()
	return &c
}

// onDemand faz uma chamada fora do ciclo (a Central de software, a padronização) pelo caminho
// certo (docs/rele.md). O ciclo escolhe direto ou relé, mas ele pode estar a minutos: na
// partida do agente, o primeiro check-in espera até 5 min; ao trocar de rede, o caminho antigo
// vale até o check-in antecipado. Por isso o caminho é conferido antes (sem novo teste se a
// escolha é recente) e, se a conexão nem abriu, testado de novo: mudou, a chamada é repetida
// uma vez. Só a conexão que não abriu repete: o servidor não chegou a ver o pedido.
func onDemand[T any](ctx context.Context, a *Agent, call func(*Client) (T, error)) (T, error) {
	a.Route.Choose(ctx, a.baseURL())
	before := a.Route.ViaRelay()
	out, err := call(a.client())
	if err == nil || ctx.Err() != nil || !connectFailed(err) {
		return out, err
	}
	a.Route.Invalidate()
	a.Route.Choose(ctx, a.baseURL())
	if a.Route.ViaRelay() == before {
		return out, err
	}
	return call(a.client())
}

// connectFailed: a conexão com o servidor nem abriu (nome que não resolve, sem rota, recusada).
func connectFailed(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// SelfServiceCatalog pede ao servidor a loja do usuário nesta máquina.
func (a *Agent) SelfServiceCatalog(ctx context.Context, u protocol.SelfServiceUser) (protocol.SelfServiceCatalog, error) {
	return onDemand(ctx, a, func(c *Client) (protocol.SelfServiceCatalog, error) { return c.SelfServiceCatalog(ctx, u) })
}

// SelfServiceRequest manda o pedido do usuário.
func (a *Agent) SelfServiceRequest(ctx context.Context, r protocol.SelfServiceRequest) (protocol.SelfServiceResponse, error) {
	return onDemand(ctx, a, func(c *Client) (protocol.SelfServiceResponse, error) { return c.SelfServiceRequest(ctx, r) })
}

// SelfServiceLogo baixa o logotipo do painel para a Central de software (Aula 19.1b).
func (a *Agent) SelfServiceLogo(ctx context.Context, limit int64) ([]byte, error) {
	return onDemand(ctx, a, func(c *Client) ([]byte, error) { return c.SelfServiceLogo(ctx, limit) })
}

// Shield pede ao servidor a "Minha Proteção" do usuário nesta máquina (docs/shield.md).
func (a *Agent) Shield(ctx context.Context, u protocol.SelfServiceUser) (protocol.ShieldView, error) {
	return onDemand(ctx, a, func(c *Client) (protocol.ShieldView, error) { return c.Shield(ctx, u) })
}
