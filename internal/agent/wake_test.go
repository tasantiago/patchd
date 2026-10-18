package agent

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// Aula 17.3: o pedido do autoatendimento aceito antecipa o check-in, sem rajada.
func TestWakeAntecipaCheckIn(t *testing.T) {
	srv, _, cred := novoServidor(t)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	a.rand = maior

	agora := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return agora }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var esperas []time.Duration
	a.sleep = func(c context.Context, d time.Duration) error {
		esperas = append(esperas, d)
		switch len(esperas) {
		case 1, 2: // as esperas longas: o Wake chega no meio
			a.Wake()
			<-c.Done() // acordado (ou encerrado)
			return c.Err()
		case 3: // o check-in anterior foi agora: espera o intervalo mínimo
			agora = agora.Add(d)
			return nil
		default:
			cancel()
			return context.Canceled
		}
	}
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := srv.conta("/api/v1/agent/checkin"); n != 2 {
		t.Errorf("check-ins = %d, esperado 2 (os dois antecipados)", n)
	}
	if len(esperas) != 4 || esperas[0] != 5*time.Minute || esperas[1] != 66*time.Minute || esperas[2] != MinWakeGap || esperas[3] != 66*time.Minute {
		t.Errorf("esperas = %v: espera inicial, intervalo interrompido, o mínimo de %v e o intervalo normal", esperas, MinWakeGap)
	}
}

func TestWakeNaoBloqueia(t *testing.T) {
	a := &Agent{}
	for range 5 {
		a.Wake() // sem Run: o primeiro fica guardado, os outros são descartados
	}
	if len(a.wakeChan()) != 1 {
		t.Errorf("Wake guarda um aviso só, guardou %d", len(a.wakeChan()))
	}
}
