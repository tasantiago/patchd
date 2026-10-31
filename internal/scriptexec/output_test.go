package scriptexec

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/bundle"
	"github.com/tasantiago/patchd/internal/jobs"
)

func TestOutputMantemApenasOFim(t *testing.T) {
	var buf tailBuffer
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for range 20 {
		if n, err := buf.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("escrita: %d %v", n, err)
		}
		if len(buf.Bytes()) > MaxOutput+1 {
			t.Fatal("log cresceu sem limite")
		}
	}
	_, _ = buf.Write([]byte("ERRO FINAL"))
	out, clipped := clip(buf.Bytes())
	if !clipped || !strings.HasSuffix(out, "ERRO FINAL") || len(out) > MaxOutput {
		t.Fatalf("fim do log: %d %v", len(out), clipped)
	}
	// O corte nunca devolve um fragmento inválido de um caractere multibyte.
	out, clipped = clip([]byte(strings.Repeat("áçã", 1000)))
	if !clipped || !utf8.ValidString(out) || len(out) > MaxOutput {
		t.Fatal("corte de UTF-8 inválido")
	}
}

func TestOutputConcorrente(t *testing.T) {
	var buf tailBuffer
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_, _ = buf.Write([]byte("linha\n"))
				_ = buf.Bytes()
			}
		}()
	}
	wg.Wait()
	if len(buf.Bytes()) > MaxOutput+1 {
		t.Fatal("buffer excedeu o limite")
	}
}

func TestExecuteTimeoutInformadoMesmoComExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture .sh")
	}
	z, sha, size := zipa(t, map[string]string{"rodar.sh": "echo exemplo"})
	e := executor(t, z)
	e.Shell = map[string]string{bundle.RunnerBash: "/bin/sh"}
	e.Run = func(ctx context.Context, _, _ string, _ []string) ([]byte, int, error) {
		<-ctx.Done()
		return nil, -1, nil // comportamento do executor anterior depois de Kill
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Execute(ctx, 1, params(sha, size, "rodar.sh", bundle.RunnerBash))
	if err == nil || errors.Is(err, jobs.ErrNotStarted) || !strings.Contains(err.Error(), "cancelada") {
		t.Fatalf("cancelamento classificado incorretamente: %v", err)
	}
}
