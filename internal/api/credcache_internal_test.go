package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestCacheDaCredencial(t *testing.T) {
	var c credCache
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	h := []byte{1, 2, 3}
	if _, ok := c.get(h, t0); ok {
		t.Fatal("vazio respondeu")
	}
	c.put(h, "m-1", t0)
	if id, ok := c.get(h, t0.Add(59*time.Second)); !ok || id != "m-1" {
		t.Errorf("dentro do prazo: %q %v", id, ok)
	}
	if _, ok := c.get(h, t0.Add(credTTL)); ok {
		t.Error("vencido respondeu")
	}
	c.put(h, "m-1", t0)
	c.reset()
	if _, ok := c.get(h, t0); ok {
		t.Error("depois do reset respondeu")
	}
	for i := 0; i < credMax; i++ {
		c.put([]byte{byte(i), byte(i >> 8), byte(i >> 16)}, "m", t0)
	}
	c.put(h, "m-2", t0) // cheio: recomeça
	if len(c.m) != 1 {
		t.Errorf("cheio: %d entradas", len(c.m))
	}
}

// O canal em tempo real segura o pedido de propósito: fica fora do tempo de resposta.
func TestCanalForaDoTempoDeResposta(t *testing.T) {
	if !transferRoute("POST "+protocol.ChannelPath) || transferRoute("POST /api/v1/agent/checkin") {
		t.Error("rotas fora do tempo de resposta")
	}
}

// A lista de pendências é calculada uma vez por minuto, mesmo com vários pedidos juntos, e o
// cálculo segue até o fim se quem pediu desistir.
func TestListaDePendenciasUmaVez(t *testing.T) {
	src := &contaItens{}
	a := &api{compliance: src, now: func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) }}
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() { _, _ = a.complianceItems(context.Background()); done <- struct{}{} }()
	}
	for i := 0; i < 5; i++ {
		<-done
	}
	if src.n != 1 {
		t.Errorf("5 pedidos juntos calcularam %d vezes", src.n)
	}
	a.now = func() time.Time { return time.Date(2026, 11, 11, 9, 1, 0, 0, time.UTC) } // venceu
	// Quem pediu desiste no meio: o cálculo segue até o fim e fica para o próximo.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := a.complianceItems(ctx); err != nil || src.n != 2 {
		t.Errorf("depois do prazo: %d cálculos, %v", src.n, err)
	}
	if _, err := a.complianceItems(context.Background()); err != nil || src.n != 2 {
		t.Errorf("o próximo não aproveitou: %d cálculos, %v", src.n, err)
	}
}

type contaItens struct {
	mu sync.Mutex
	n  int
}

func (c *contaItens) Fleet(context.Context) (protocol.ComplianceFleet, error) {
	return protocol.ComplianceFleet{}, nil
}
func (c *contaItens) Detail(context.Context, string) (protocol.ComplianceDetail, bool, error) {
	return protocol.ComplianceDetail{}, false, nil
}
func (c *contaItens) Items(context.Context) ([]protocol.MachineItems, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return []protocol.MachineItems{{MachineID: "m-1"}}, nil
}
