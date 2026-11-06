package api

import (
	"io"
	"net/http"

	"github.com/tasantiago/patchd/internal/canal"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O canal em tempo real (internal/canal): o agente espera aqui, e o servidor o acorda quando
// surge um job para a máquina. Ligado por padrão; Configuração › Parâmetros › Agentes desliga.

// ChannelKey: o interruptor do canal em Parâmetros.
const ChannelKey = "PATCHD_CANAL"

// WithChannel liga o canal com o hub (o servidor alimenta o hub pelo LISTEN do PostgreSQL).
func WithChannel(h *canal.Hub) Option {
	return func(a *api) { a.channel = h }
}

// channelOn: o canal existe e não foi desligado na tela.
func (a *api) channelOn() bool {
	if a.channel == nil {
		return false
	}
	return a.params == nil || a.params.Value(ChannelKey) != "0"
}

// channelWait: POST /api/v1/agent/canal.
func (a *api) channelWait(w http.ResponseWriter, r *http.Request, id string) {
	if !a.channelOn() {
		writeJSON(w, http.StatusOK, protocol.ChannelResponse{})
		return
	}
	// Lê o corpo (vazio) antes de esperar: só com ele lido o servidor HTTP passa a vigiar a
	// conexão e cancela o contexto quando o agente desiste (reiniciou, perdeu a rede).
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	woke, reason := a.channel.Wait(r.Context(), id, canal.HoldFor)
	if r.Context().Err() != nil {
		return // o agente desistiu (reiniciou, perdeu a rede): ninguém para ler
	}
	writeJSON(w, http.StatusOK, protocol.ChannelResponse{Enabled: a.channelOn(), Wake: woke, Reason: reason})
}
