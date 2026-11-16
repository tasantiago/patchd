package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// A fila dos agentes (docs/saude-servidor.md, quarta rodada). Com o pool do banco cheio, as
// requisições dos agentes e as telas do painel esperavam conexão na mesma fila: abrir uma
// máquina no Inventário (várias consultas, cada uma atrás de dezenas de check-ins) levava
// dezenas de segundos. Aqui as rotas dos agentes que usam o banco entram, no máximo, slots de
// cada vez; o resto do pool fica para o painel e para os trabalhos do próprio servidor (o
// motor das campanhas, a manutenção). Quem não acha vaga espera na fila da rota (uma goroutine
// parada, sem conexão do banco) e, passado wait, recebe 503 com Retry-After: o agente tenta
// de novo depois, em vez de o servidor fazer o trabalho de um pedido cuja resposta ninguém
// mais espera (o prazo de escrita do servidor é de 60 s).
//
// Ficam de fora o canal em tempo real (não usa o banco: a credencial vem do cache) e, nos
// downloads, tudo depois da conferência da credencial (a transferência leva minutos e não
// segura conexão do banco).

// gateMode: quanto da requisição a vaga cobre.
type gateMode int

const (
	gateNone gateMode = iota // sem fila (o canal)
	gateAuth                 // só a conferência da credencial (os downloads)
	gateFull                 // a requisição inteira
)

// agentGateWait: o máximo na fila, menor que o prazo de escrita do servidor.
const agentGateWait = 30 * time.Second

type agentGate struct {
	slots chan struct{}
	wait  time.Duration

	waited   atomic.Int64 // esperaram por vaga
	rejected atomic.Int64 // desistiram (503)
	waitNs   atomic.Int64 // tempo somado na fila
}

func newAgentGate(slots int, wait time.Duration) *agentGate {
	if slots <= 0 {
		return nil
	}
	if wait <= 0 {
		wait = agentGateWait
	}
	return &agentGate{slots: make(chan struct{}, slots), wait: wait}
}

// WithAgentGate liga a fila dos agentes: slots requisições com o banco de cada vez.
func WithAgentGate(slots int) Option {
	return func(a *api) { a.gate = newAgentGate(slots, agentGateWait) }
}

// AgentGateSlots: quantas requisições de agente usam o banco de uma vez, para um pool de max
// conexões. Reserva um quarto do pool mais uma (no mínimo três) para o painel e os trabalhos
// do servidor: a avaliação do parque sozinha usa um quarto do pool.
func AgentGateSlots(max int) int {
	if max < 4 {
		return 0 // pool pequeno demais para dividir: sem fila
	}
	return max - (max/4 + 1)
}

// acquire espera uma vaga. ok=false: o prazo passou ou quem pediu foi embora.
func (g *agentGate) acquire(ctx context.Context) (release func(), ok bool) {
	if g == nil {
		return func() {}, true
	}
	select {
	case g.slots <- struct{}{}:
		return g.release, true
	default:
	}
	start := time.Now()
	g.waited.Add(1)
	t := time.NewTimer(g.wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		g.waitNs.Add(int64(time.Since(start)))
		return g.release, true
	case <-t.C:
	case <-ctx.Done():
	}
	g.waitNs.Add(int64(time.Since(start)))
	g.rejected.Add(1)
	return nil, false
}

func (g *agentGate) release() { <-g.slots }

// gateStats: o retrato da fila para a aba Saúde.
type gateStats struct {
	Slots, InUse     int
	Waited, Rejected int64
	AvgWait          time.Duration
}

func (g *agentGate) stats() (gateStats, bool) {
	if g == nil {
		return gateStats{}, false
	}
	s := gateStats{Slots: cap(g.slots), InUse: len(g.slots), Waited: g.waited.Load(), Rejected: g.rejected.Load()}
	if s.Waited > 0 {
		s.AvgWait = time.Duration(g.waitNs.Load() / s.Waited)
	}
	return s, true
}

// busy responde a quem não conseguiu vaga.
func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "30")
	writeError(w, http.StatusServiceUnavailable, "busy", "o servidor está ocupado; tente de novo em instantes")
}
