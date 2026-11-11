package api

import (
	"context"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// itemsCache: a lista de pendências do parque (compliance.Items), feita uma vez e
// reaproveitada por itemsTTL. As telas de Patch (Sistema, Visão geral, Terceiros, os lotes)
// pediam a lista inteira a cada clique: com o Windows atrasado, são milhões de itens e mais de
// 1 GB de memória por pedido, e quem clicava de novo porque a tela "travou" começava outro
// cálculo igual por cima. Agora há um cálculo por vez; quem chega no meio espera o mesmo, e o
// cálculo segue até o fim mesmo se quem pediu desistir (o próximo clique o aproveita).
type itemsCache struct {
	once sync.Once
	sem  chan struct{} // a vaga do cálculo

	mu   sync.Mutex
	at   time.Time
	list []protocol.MachineItems
	agg  []patchItem // a lista por item, sem as aprovações (aggregateItems)
}

// itemsTTL: quanto a lista vale. Um inventário ou uma busca nova aparecem no próximo cálculo.
const itemsTTL = time.Minute

func (c *itemsCache) init() { c.once.Do(func() { c.sem = make(chan struct{}, 1) }) }

func (c *itemsCache) fresh(now time.Time) ([]protocol.MachineItems, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil && now.Sub(c.at) < itemsTTL && !now.Before(c.at) {
		return c.list, true
	}
	return nil, false
}

// complianceItems devolve a lista do parque (a guardada, ou uma nova).
func (a *api) complianceItems(ctx context.Context) ([]protocol.MachineItems, error) {
	c := &a.items
	c.init()
	if l, ok := c.fresh(a.now()); ok {
		return l, nil
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	if l, ok := c.fresh(a.now()); ok { // quem segurava a vaga acabou de calcular
		return l, nil
	}
	// O cálculo não depende de quem pediu: segue se ele desistir.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), complianceTimeout)
	defer cancel()
	l, err := a.compliance.Items(work)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.list, c.agg, c.at = l, nil, a.now()
	c.mu.Unlock()
	return l, nil
}

// fleetItems avalia a frota e junta por item, com as aprovações de agora. A junção (a parte
// cara) é guardada com a lista; as aprovações entram a cada pedido, numa cópia.
func (a *api) fleetItems(ctx context.Context) ([]patchItem, error) {
	ctx, cancel := context.WithTimeout(ctx, complianceTimeout)
	defer cancel()
	list, err := a.complianceItems(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := a.store.PatchApprovals(ctx)
	if err != nil {
		return nil, err
	}
	return a.aggregated(list, approvals), nil
}

// aggregated: a lista por item (aggregateItems) da lista do parque, guardada junto com ela,
// com as aprovações de agora.
func (a *api) aggregated(list []protocol.MachineItems, approvals []store.PatchApproval) []patchItem {
	c := &a.items
	c.mu.Lock()
	agg := c.agg
	if agg == nil || !sameList(c.list, list) {
		agg = aggregateItems(list, nil)
		if sameList(c.list, list) {
			c.agg = agg
		}
	}
	c.mu.Unlock()
	return withApprovals(agg, approvals)
}

func sameList(a, b []protocol.MachineItems) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// withApprovals copia os itens (as listas de máquinas são as mesmas, só de leitura) e liga a
// aprovação de cada um.
func withApprovals(agg []patchItem, approvals []store.PatchApproval) []patchItem {
	approved := make(map[string]store.PatchApproval, len(approvals))
	for _, ap := range approvals {
		approved[store.ApprovalKey(ap.Item, ap.FixedIn)] = ap
	}
	out := make([]patchItem, len(agg))
	for i, p := range agg {
		out[i] = p
		if ap, ok := approved[store.ApprovalKey(p.ID, p.FixedIn)]; ok {
			out[i].Approval = &ap
		}
	}
	return out
}
