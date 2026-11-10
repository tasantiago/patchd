package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O desligar (Aula 22.3): só sem ninguém conectado; com alguém (mesmo sessão desconectada),
// conta e não desliga.
func TestRunShutdowns(t *testing.T) {
	desligou := 0
	a := &Agent{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shutdown: func(context.Context, int64) error { desligou++; return nil }}
	a.defaults()
	a.LoggedUsers = func(context.Context) ([]protocol.LoggedUser, error) {
		return []protocol.LoggedUser{{Account: `TJ\fulano`, Active: false}}, nil
	}
	out := a.runShutdowns(context.Background(), []int64{7})
	if desligou != 0 || out[0].Result.Status != protocol.JobResultOK || !strings.Contains(out[0].Result.Detail, `não desligou: sessão aberta de TJ\fulano`) {
		t.Fatalf("com alguém: %d %+v", desligou, out)
	}
	a.LoggedUsers = func(context.Context) ([]protocol.LoggedUser, error) { return nil, nil }
	out = a.runShutdowns(context.Background(), []int64{8, 9})
	if desligou != 1 || len(out) != 2 || !strings.HasPrefix(out[1].Result.Detail, "desligando em 2 min") {
		t.Fatalf("sem ninguém: %d %+v", desligou, out)
	}
	a.LoggedUsers = func(context.Context) ([]protocol.LoggedUser, error) { return nil, errors.New("WTS") }
	if out := a.runShutdowns(context.Background(), []int64{10}); desligou != 1 || out[0].Result.Status != protocol.JobResultFailed {
		t.Errorf("sem saber quem está: %+v", out)
	}
}
