package tpexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
)

// O job desinstalar: a mesma remoção da regularização, com o log do job; ocupado: adiado.
func TestJobDesinstalar(t *testing.T) {
	p := jobs.UninstallParams{ReplaceEntry: jobs.ReplaceEntry{Name: "Programa", Version: "1.0", Method: "quiet",
		Key: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Programa`}}
	var log string
	e := &Executor{LogDir: `C:\dados\logs`, Remove: func(_ context.Context, r jobs.ReplaceEntry, l string) (string, error) {
		log = l
		return "", nil
	}}
	if d, err := e.Uninstall(context.Background(), 9, p); err != nil || !strings.Contains(d, "Programa 1.0 removido") || !strings.HasSuffix(log, "remover-job-9.log") {
		t.Fatalf("remover: %q %v %q", d, err, log)
	}
	e.Remove = func(context.Context, jobs.ReplaceEntry, string) (string, error) {
		return "a instalação anterior já não estava registrada", nil
	}
	if d, _ := e.Uninstall(context.Background(), 9, p); !strings.Contains(d, "Programa já não estava instalado") {
		t.Errorf("já não estava: %q", d)
	}
	e.InstallerBusy = func() (bool, error) { return true, nil }
	if _, err := e.Uninstall(context.Background(), 9, p); !errors.Is(err, ErrRetry) {
		t.Errorf("ocupado: %v", err)
	}
	e.InstallerBusy, e.Remove = nil, nil
	if _, err := e.Uninstall(context.Background(), 9, p); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("sem remoção: %v", err)
	}
	if _, err := (&Executor{Remove: e.Remove}).Uninstall(context.Background(), 9, jobs.UninstallParams{}); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("entrada inválida: %v", err)
	}
}
