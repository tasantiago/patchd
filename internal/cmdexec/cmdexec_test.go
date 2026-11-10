package cmdexec

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/tasantiago/patchd/internal/jobs"
)

func TestArgs(t *testing.T) {
	a := Args(jobs.CommandParams{Shell: jobs.ShellPowerShell, Command: `Get-Service "Spooler" | fl`})
	raw, err := base64.StdEncoding.DecodeString(a[len(a)-1])
	if err != nil || a[len(a)-2] != "-EncodedCommand" || len(raw)%2 != 0 {
		t.Fatalf("%v %v", a, err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if s := string(utf16.Decode(u)); !strings.HasSuffix(s, `;Get-Service "Spooler" | fl`) || !strings.Contains(s, "OutputEncoding") {
		t.Errorf("powershell: %q", s)
	}
	if a := Args(jobs.CommandParams{Shell: jobs.ShellCmd, Command: `dir "C:\Program Files"`}); strings.Join(a, " ") != `/d /s /c "chcp 65001>nul & dir "C:\Program Files""` {
		t.Errorf("cmd: %q", a)
	}
	if a := Args(jobs.CommandParams{Shell: jobs.ShellBash, Command: "df -h"}); len(a) != 2 || a[0] != "-c" || a[1] != "df -h" {
		t.Errorf("bash: %q", a)
	}
}

func TestExecute(t *testing.T) {
	r := &Runner{Shells: map[string]string{jobs.ShellBash: "/bin/sh"}, Dir: "/"}
	// Interpretador que a máquina não tem: recusa, nada roda.
	if _, err := r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellPowerShell, Command: "x", TimeoutSec: 5}); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("sem o interpretador: %v", err)
	}
	if _, err := r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellBash, Command: "", TimeoutSec: 5}); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("comando vazio: %v", err)
	}
	// O falso: saída maior que o limite fica com o fim; o código de saída volta.
	r.Run = func(_ context.Context, dir, name string, args []string, max int) ([]byte, int, error) {
		if dir != "/" || name != "/bin/sh" || max != jobs.MaxCommandOutput {
			t.Errorf("chamada: %s %s %d", dir, name, max)
		}
		return []byte(strings.Repeat("linha\n", 20000) + "fim"), 3, errors.New("exit status 3")
	}
	res, err := r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellBash, Command: "x", TimeoutSec: 5})
	if err != nil || res.ExitCode != 3 || !res.Truncated || !strings.HasSuffix(res.Output, "fim") || len(res.Output) > jobs.MaxCommandOutput {
		t.Fatalf("%+v %v (%d bytes)", res, err, len(res.Output))
	}
	if runtime.GOOS == "windows" {
		return
	}
	// De verdade: a saída, o código, e o tempo máximo encerrando o que não termina.
	r.Run = nil
	res, err = r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellBash, Command: "echo olá; echo erro >&2; exit 7", TimeoutSec: 5})
	if err != nil || res.ExitCode != 7 || res.Output != "olá\nerro" || res.TimedOut {
		t.Fatalf("de verdade: %+v %v", res, err)
	}
	inicio := time.Now()
	res, err = r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellBash, Command: "echo antes; sleep 60", TimeoutSec: jobs.MinCommandTimeout})
	if err != nil || !res.TimedOut || res.ExitCode != -1 || !strings.Contains(res.Output, "antes") || time.Since(inicio) > 20*time.Second {
		t.Fatalf("tempo máximo: %+v %v em %v", res, err, time.Since(inicio))
	}
	// Sem teclado: o comando que espera entrada recebe o fim de arquivo e termina.
	if res, _ := r.Execute(context.Background(), jobs.CommandParams{Shell: jobs.ShellBash, Command: "read x; echo \"[$x]\"", TimeoutSec: 5}); res.Output != "[]" || res.TimedOut {
		t.Errorf("sem entrada: %+v", res)
	}
}
