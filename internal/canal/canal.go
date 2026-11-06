// Package canal é o canal em tempo real entre o servidor e os agentes (long-poll). Cada
// agente mantém uma requisição aberta (POST /api/v1/agent/canal), de dentro para fora, na
// mesma porta do check-in; o servidor a segura até HoldFor e responde "venha agora" assim
// que surge algo para a máquina (um job novo, um job agendado que venceu). Sem nada, responde
// "nada" e o agente abre outra. Nenhuma porta nova, nenhuma conexão de entrada na estação.
//
// O Hub vive na memória de cada processo do servidor: o aviso chega a ele pelo LISTEN do
// PostgreSQL (o gatilho na tabela de jobs), então dois servidores atrás de um balanceador
// acordam cada um os agentes conectados a si.
package canal

import (
	"context"
	"sync"
	"time"
)

// HoldFor: quanto o servidor segura a requisição sem novidade. Abaixo do prazo de escrita do
// servidor (60 s) e do corte de conexão parada da maioria dos firewalls e proxies.
const HoldFor = 45 * time.Second

// pendingFor: um aviso que chegou com a máquina fora da espera (entre uma requisição e a
// seguinte, ou reconectando) vale para a próxima espera dentro deste prazo.
const pendingFor = 2 * time.Minute

// Hub junta as esperas por máquina.
type Hub struct {
	mu      sync.Mutex
	waiting map[string]*waiter
	pending map[string]time.Time
	closed  bool
	now     func() time.Time
}

type waiter struct {
	ch chan string // o motivo; fechado sem motivo: substituído ou servidor encerrando
}

// New cria o hub.
func New() *Hub {
	return &Hub{waiting: map[string]*waiter{}, pending: map[string]time.Time{}, now: time.Now}
}

// Notify avisa a máquina: acorda a espera dela, ou guarda o aviso para a próxima.
func (h *Hub) Notify(machineID, reason string) {
	if h == nil || machineID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if w := h.waiting[machineID]; w != nil {
		delete(h.waiting, machineID)
		w.ch <- reason
		close(w.ch)
		return
	}
	h.pending[machineID] = h.now()
	if len(h.pending) > 50_000 { // sem crescer sem fim com avisos de máquinas que nunca conectam
		h.sweep()
	}
}

func (h *Hub) sweep() {
	for id, at := range h.pending {
		if h.now().Sub(at) > pendingFor {
			delete(h.pending, id)
		}
	}
}

// Wait espera um aviso para a máquina por até d. woke: houve aviso (reason diz qual). Uma
// segunda espera da mesma máquina (o agente reiniciou) solta a primeira, sem aviso.
func (h *Hub) Wait(ctx context.Context, machineID string, d time.Duration) (woke bool, reason string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false, ""
	}
	if at, ok := h.pending[machineID]; ok {
		delete(h.pending, machineID)
		if h.now().Sub(at) <= pendingFor {
			h.mu.Unlock()
			return true, "aviso guardado"
		}
	}
	if old := h.waiting[machineID]; old != nil {
		close(old.ch)
	}
	w := &waiter{ch: make(chan string, 1)}
	h.waiting[machineID] = w
	h.mu.Unlock()

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r, ok := <-w.ch:
		return ok && r != "", r
	case <-t.C:
	case <-ctx.Done():
	}
	h.mu.Lock()
	if h.waiting[machineID] == w {
		delete(h.waiting, machineID)
	}
	h.mu.Unlock()
	return false, ""
}

// Connected: quantas máquinas estão esperando agora (conectadas ao canal).
func (h *Hub) Connected() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.waiting)
}

// IsConnected diz se a máquina está esperando agora.
func (h *Hub) IsConnected(machineID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waiting[machineID] != nil
}

// Close solta todas as esperas (o servidor vai encerrar: o Shutdown do HTTP não espera 45 s).
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, w := range h.waiting {
		close(w.ch)
		delete(h.waiting, id)
	}
}
