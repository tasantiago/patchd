//go:build linux || darwin

package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerForcaLocaleC(t *testing.T) {
	t.Setenv("LC_ALL", "pt_BR.UTF-8")
	out, err := ExecRunner{}.Run(context.Background(), "/bin/sh", "-c", "echo $LC_ALL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "C" {
		t.Errorf("LC_ALL = %q, esperado %q", got, "C")
	}
}

func TestExecRunnerRecusaCaminhoRelativo(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), "sh", "-c", "true")
	if err == nil || !strings.Contains(err.Error(), "caminho absoluto") {
		t.Errorf("esperado erro de caminho absoluto, veio %v", err)
	}
}

func TestExecRunnerPrazo(t *testing.T) {
	inicio := time.Now()
	_, err := ExecRunner{Timeout: 200 * time.Millisecond}.Run(context.Background(), "/bin/sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "prazo") {
		t.Errorf("esperado erro de prazo, veio %v", err)
	}
	if d := time.Since(inicio); d > 2*time.Second {
		t.Errorf("o comando não foi interrompido a tempo: %s", d)
	}
}

func TestExecRunnerStderrNoErro(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), "/bin/sh", "-c", "echo falhou >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "falhou") {
		t.Errorf("esperado erro com o stderr, veio %v", err)
	}
}
