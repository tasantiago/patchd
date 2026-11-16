package api

import (
	"context"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// A lista do Inventário guardada por alguns segundos (docs/saude-servidor.md, quarta rodada).
// A busca rápida do topo lê a lista inteira para achar a máquina e redireciona para uma tela
// que a lê de novo; quem pagina ou troca o filtro lê outra vez a cada clique. Com o banco
// ocupado, cada leitura custa. Guardada, as telas seguintes saem da memória.
//
// Só as telas de leitura usam (a lista, o CSV e a busca). O que decide algo (as regras dos
// grupos de implantação, a mescla, os lotes) lê do banco. Qualquer formulário do painel zera
// a cópia: quem acabou de aposentar ou mover uma máquina vê o resultado na volta.

// DefaultListCacheTTL: o atraso máximo de um check-in novo aparecer na lista.
const DefaultListCacheTTL = 15 * time.Second

type listCache struct {
	ttl time.Duration // 0: desligado (os testes e o servidor em memória)

	mu   sync.Mutex
	rows []store.InventoryRow
	at   time.Time
}

// WithListCache liga a cópia da lista do Inventário por ttl.
func WithListCache(ttl time.Duration) Option {
	return func(a *api) { a.invList.ttl = ttl }
}

// inventoryList: a lista, da cópia se ela ainda vale. Quem chama só lê as linhas. O mutex
// fica preso durante a leitura do banco de propósito: dez telas pedindo juntas fazem uma
// consulta, não dez.
func (a *api) inventoryList(ctx context.Context) ([]store.InventoryRow, error) {
	c := &a.invList
	if c.ttl <= 0 {
		return a.store.InventoryList(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := a.now(); !c.at.IsZero() && !now.Before(c.at) && now.Sub(c.at) < c.ttl {
		return c.rows, nil
	}
	// Sem o cancelamento de quem pediu: a leitura serve a todos os que esperam o mutex.
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rows, err := a.store.InventoryList(qctx)
	if err != nil {
		return nil, err
	}
	c.rows, c.at = rows, a.now()
	return rows, nil
}

// listChanged zera a cópia (depois de um formulário do painel).
func (a *api) listChanged() {
	c := &a.invList
	c.mu.Lock()
	c.rows, c.at = nil, time.Time{}
	c.mu.Unlock()
}
