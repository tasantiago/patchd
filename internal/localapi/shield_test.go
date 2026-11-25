package localapi

import (
	"context"
	"errors"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// backendShield: o backend com a "Minha Proteção".
type backendShield struct {
	*backend
	v    protocol.ShieldView
	err  error
	quem protocol.SelfServiceUser
}

func (b *backendShield) Shield(_ context.Context, u protocol.SelfServiceUser) (protocol.ShieldView, error) {
	b.quem = u
	return b.v, b.err
}

func TestServeShield(t *testing.T) {
	ctx := context.Background()
	id := Identity{Account: `EXEMPLO\ana`, SID: "S-1-5-21-1-2-3-1001", Groups: []string{`EXEMPLO\g`}}
	// Backend sem Shield (agente de teste): desligado, não falha interna.
	s, _, _ := servidor(&backend{})
	if r := s.Serve(ctx, id, Request{Op: OpShield}); r.OK || r.Code != CodeOff {
		t.Fatalf("sem Shield: %+v", r)
	}
	b := &backendShield{backend: &backend{}, v: protocol.ShieldView{Enabled: true, Machine: &protocol.ShieldMachineView{Display: 90}}}
	s, _, _ = servidor(b)
	r := s.Serve(ctx, id, Request{Op: OpShield})
	if !r.OK || r.Shield == nil || r.Shield.Machine.Display != 90 {
		t.Fatalf("%+v", r)
	}
	// A identidade é a do token, não do pedido.
	if b.quem.Account != id.Account || b.quem.SID != id.SID {
		t.Fatalf("identidade: %+v", b.quem)
	}
	// Resposta fora dos limites (100 sem selo) não chega à Central.
	b.v.Machine.Display = 100
	if r := s.Serve(ctx, id, Request{Op: OpShield}); r.OK || r.Code != CodeServer {
		t.Fatalf("100 sem selo passou: %+v", r)
	}
	b.v.Machine.Display, b.err = 50, errors.New("dial tcp: recusada")
	if r := s.Serve(ctx, id, Request{Op: OpShield}); r.OK || r.Code != CodeUnavailable {
		t.Fatalf("servidor fora: %+v", r)
	}
	// O limite de consultas vale para o Shield também.
	b.err = nil
	for range QueryLimit + 2 {
		r = s.Serve(ctx, id, Request{Op: OpShield})
	}
	if r.OK || r.Code != CodeLimit {
		t.Fatalf("sem limite: %+v", r)
	}
}
