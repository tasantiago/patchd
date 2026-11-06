package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O canal em tempo real (internal/canal, no servidor): com o servidor dizendo no check-in que
// o canal está ligado, o agente mantém uma espera aberta (POST /api/v1/agent/canal, de dentro
// para fora, na porta de sempre). Quando o servidor responde "venha", o agente faz o check-in
// na hora e recebe o job. Sem novidade, a espera volta em ~45 s e o agente abre outra.

// ReasonChannel: o motivo do check-in antecipado pelo canal.
const ReasonChannel = "canal"

const (
	channelRetry    = 30 * time.Second // depois de uma falha de rede
	channelMaxRetry = 5 * time.Minute
)

// Channel abre uma espera no canal.
func (c *Client) Channel(ctx context.Context) (protocol.ChannelResponse, error) {
	var out protocol.ChannelResponse
	_, err := c.post(ctx, protocol.ChannelPath, struct{}{}, &out)
	return out, err
}

// setChannel liga ou desliga a espera (o que o check-in disse).
func (a *Agent) setChannel(on bool) {
	if a.channelOn.Swap(on) != on && on {
		select {
		case a.channelKickChan() <- struct{}{}:
		default:
		}
	}
}

func (a *Agent) channelKickChan() chan struct{} {
	a.channelOnce.Do(func() { a.channelKick = make(chan struct{}, 1) })
	return a.channelKick
}

// channelLoop mantém a espera aberta enquanto o canal estiver ligado.
func (a *Agent) channelLoop(ctx context.Context) {
	retry := channelRetry
	for ctx.Err() == nil {
		if !a.channelOn.Load() {
			select {
			case <-ctx.Done():
				return
			case <-a.channelKickChan():
			}
			continue
		}
		resp, err := a.Client.Channel(ctx)
		if ctx.Err() != nil {
			return
		}
		var rej *RejectedError
		switch {
		case errors.Is(err, ErrUnauthorized), errors.As(err, &rej) && rej.Status == http.StatusNotFound:
			// Credencial recusada, ou servidor sem canal: só o check-in, até ele dizer o contrário.
			a.channelOn.Store(false)
			continue
		case err != nil:
			a.Logger.Debug("canal em tempo real: espera falhou; nova tentativa", "error", err, "em", retry.String())
			if a.sleep(ctx, retry) != nil {
				return
			}
			retry = min(2*retry, channelMaxRetry)
			continue
		}
		retry = channelRetry
		if !resp.Enabled {
			a.channelOn.Store(false)
			continue
		}
		if resp.Wake {
			a.Logger.Info("canal em tempo real: o servidor chamou", "motivo", resp.Reason)
			a.wakeFor(ReasonChannel)
		}
	}
}
