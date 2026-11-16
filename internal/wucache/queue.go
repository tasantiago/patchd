package wucache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A fila do horário (docs/cache-windows.md, "Baixar da Microsoft de madrugada"). Com o
// Schedule configurado, o servidor só baixa da Microsoft dentro do horário: o download de
// alguns GB não disputa o link de internet e o disco com o expediente. O pedido feito fora do
// horário vira um arquivo pequeno na pasta do cache (<chave>.fila, com o endereço); quando o
// horário abre, Drain começa os downloads. A entrega às máquinas do que já está no cache não
// muda: vale a qualquer hora.

const (
	queueSuffix = ".fila"
	// queueKeep: o pedido que ninguém repetiu nesse prazo sai da fila (Prune).
	queueKeep = 7 * 24 * time.Hour
	// QueueEvery: de quanto em quanto tempo o servidor confere a fila.
	QueueEvery = time.Minute
)

// Queued é um pedido na fila.
type Queued struct {
	Key   string    `json:"key"`
	URL   string    `json:"url"`
	Asked time.Time `json:"asked"`
}

// enqueue guarda o pedido, se ainda não está na fila; o pedido repetido renova a data (a
// limpeza conta por ela).
func (c *Cache) enqueue(key, raw string) error {
	name := c.file(key) + queueSuffix
	now := c.now()
	if _, err := os.Stat(name); err == nil {
		return os.Chtimes(name, now, now)
	}
	b, err := json.Marshal(Queued{Key: key, URL: raw, Asked: now.UTC()})
	if err != nil {
		return err
	}
	// #nosec G306 -- endereço público de download da Microsoft, sem segredo
	if err := os.WriteFile(name, b, 0o640); err != nil {
		return err
	}
	return os.Chtimes(name, now, now) // a limpeza conta pelo relógio do cache
}

func (c *Cache) dequeue(key string) { _ = os.Remove(c.file(key) + queueSuffix) }

// Queue lista a fila, do pedido mais antigo para o mais novo.
func (c *Cache) Queue() []Queued {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil
	}
	var out []Queued
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), queueSuffix)
		if !ok || !ValidKey(key) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.Dir, e.Name())) // #nosec G304 -- nome conferido (ValidKey), na pasta do cache
		var q Queued
		if err != nil || json.Unmarshal(b, &q) != nil || q.Key != key {
			continue
		}
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b Queued) int { return a.Asked.Compare(b.Asked) })
	return out
}

// Drain começa os downloads da fila, se o horário está aberto. Devolve quantos começou. O
// endereço guardado passa pela mesma conferência do pedido (Parse): a fila não vira um jeito
// de baixar de outro lugar. O download em curso quando o horário fecha vai até o fim.
func (c *Cache) Drain() int {
	c.init()
	if c.Schedule != nil {
		if open, _ := c.Schedule(c.now()); !open {
			return 0
		}
	}
	started := 0
	for _, q := range c.Queue() {
		u, key, err := Parse(q.URL)
		if err != nil || key != q.Key {
			c.dequeue(q.Key)
			continue
		}
		if _, ok := c.Ready(key); ok {
			c.dequeue(key)
			continue
		}
		c.mu.Lock()
		_, running := c.running[key]
		f, failed := c.failed[key]
		skip := running || (failed && c.now().Sub(f.at) < retryAfter)
		if !skip {
			delete(c.failed, key)
			c.running[key] = &progress{url: u.String()}
		}
		c.mu.Unlock()
		if skip {
			continue
		}
		go c.fetch(u, key)
		started++
	}
	if started > 0 {
		c.Logger.Info("cache do Windows: horário aberto, baixando a fila da Microsoft", "arquivos", started)
	}
	return started
}

// RunQueue confere a fila a cada every até o ctx acabar.
func (c *Cache) RunQueue(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Drain()
		}
	}
}
