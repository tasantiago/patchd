package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

type runnerGravador struct{ linhas []string }

func (r *runnerGravador) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.linhas = append(r.linhas, name+" "+strings.Join(args, " | "))
	return nil, nil
}

func TestReinicioAgendado(t *testing.T) {
	r := &runnerGravador{}
	if err := newRestart(r)(context.Background(), 42, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(r.linhas) != 1 || !strings.HasPrefix(r.linhas[0], "/usr/sbin/shutdown -r | +15 | patchd (job 42): esta máquina vai reiniciar em 15 minuto(s)") {
		t.Errorf("%v", r.linhas)
	}
}
