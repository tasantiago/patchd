//go:build linux || darwin

package scriptexec

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunLocalEncerraFilhosNoTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, _, err := RunLocal(ctx, t.TempDir(), "/bin/sh", []string{"-c", "sleep 120 &\necho $!\nwait"})
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("timeout não interrompeu: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("PID do filho: %q %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// No Linux o filho morto pode aguardar reap pelo init do contêiner como zombie.
	// Em macOS, um processo vivo que recebeu SIGKILL ainda pode levar alguns ms para sair.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("o filho continuou rodando depois do timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunLocalPreservaArgumentos(t *testing.T) {
	want := `C:\Program Files\Empresa\`
	out, code, err := RunLocal(context.Background(), t.TempDir(), "/bin/sh", []string{"-c", `printf '%s' "$1"`, "exemplo", want})
	if err != nil || code != 0 || string(out) != want {
		t.Fatalf("argumento alterado: %q %d %v", out, code, err)
	}
}
