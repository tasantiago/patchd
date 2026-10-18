package agent

import (
	"context"

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

// SelfServiceCatalog pede ao servidor a loja do usuário nesta máquina.
func (a *Agent) SelfServiceCatalog(ctx context.Context, u protocol.SelfServiceUser) (protocol.SelfServiceCatalog, error) {
	return a.client().SelfServiceCatalog(ctx, u)
}

// SelfServiceRequest manda o pedido do usuário.
func (a *Agent) SelfServiceRequest(ctx context.Context, r protocol.SelfServiceRequest) (protocol.SelfServiceResponse, error) {
	return a.client().SelfServiceRequest(ctx, r)
}
