package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

type runnerGravadorWin struct{ linhas []string }

func (r *runnerGravadorWin) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.linhas = append(r.linhas, name+" "+strings.Join(args, " | "))
	return nil, nil
}

func TestReinicioAgendadoWindows(t *testing.T) {
	r := &runnerGravadorWin{}
	if err := newRestart(r)(context.Background(), 7, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(r.linhas) != 1 || !strings.HasSuffix(strings.SplitN(r.linhas[0], " ", 2)[0], `System32\shutdown.exe`) ||
		!strings.Contains(r.linhas[0], "/r | /t | 900 | /d | p:2:17 | /c | patchd (job 7): este computador vai reiniciar em 15 minuto(s)") {
		t.Errorf("%v", r.linhas)
	}
}
