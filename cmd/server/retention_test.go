package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type pruneFalso struct {
	chamadas chan time.Time
}

func (p pruneFalso) PruneScans(ctx context.Context, before time.Time) (int64, error) {
	p.chamadas <- before
	return 3, nil
}

func TestPruneScansLoopRodaNaPartidaEParaNoEncerramento(t *testing.T) {
	p := pruneFalso{chamadas: make(chan time.Time, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	fim := make(chan struct{})
	go func() {
		pruneScansLoop(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), p)
		close(fim)
	}()

	select {
	case before := <-p.chamadas:
		if d := time.Since(before); d < 89*24*time.Hour || d > 91*24*time.Hour {
			t.Errorf("corte da retenção em ~90 dias: %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a retenção deveria rodar na partida")
	}
	cancel()
	select {
	case <-fim:
	case <-time.After(2 * time.Second):
		t.Fatal("o laço deveria parar no encerramento")
	}
}
