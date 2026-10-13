//go:build !windows

package platform

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// Um instalador que termina com código 3 mas deixa um filho rodando: o SilentRunner volta
// logo, com o código; o ExecRunner esperaria o filho (o pipe da saída fica aberto).
func TestSilentRunnerNaoEsperaFilho(t *testing.T) {
	start := time.Now()
	_, err := SilentRunner{Timeout: 20 * time.Second}.Run(context.Background(), "/bin/sh", "-c", "sleep 5 & exit 3")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("código: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("esperou o filho: %s", d)
	}
	if _, err := (SilentRunner{Timeout: 200 * time.Millisecond}).Run(context.Background(), "/bin/sleep", "5"); err == nil ||
		err.Error() != "/bin/sleep: prazo de 200ms esgotado" {
		t.Errorf("prazo: %v", err)
	}
	if _, err := (SilentRunner{}).Run(context.Background(), "sh"); err == nil {
		t.Error("nome relativo devia ser recusado")
	}
}
