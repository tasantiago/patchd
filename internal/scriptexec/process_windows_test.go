package scriptexec

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/bundle"
	"golang.org/x/sys/windows"
)

func TestBatchCaminhoEArgumentoComEspacos(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pasta com espacos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "instalar exemplo.cmd")
	if err := os.WriteFile(entry, []byte("@echo off\r\necho %~1\r\nexit /b 0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := `C:\Program Files\Empresa`
	name, args := command(DefaultShells()[bundle.RunnerCmd], bundle.RunnerCmd, entry, []string{want})
	out, code, err := RunLocal(context.Background(), dir, name, args)
	if err != nil || code != 0 || strings.TrimSpace(string(out)) != want {
		t.Fatalf("Batch alterou caminho/parâmetro: %q %d %v", out, code, err)
	}
}

func TestWindowsEncerraFilhosNoTimeout(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "filho.ps1")
	if err := os.WriteFile(child, []byte("Start-Sleep -Seconds 120"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "pai.ps1")
	script := `$p = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList @('-NoProfile', '-File', ('"' + (Join-Path $PSScriptRoot 'filho.ps1') + '"')) -PassThru
Write-Output $p.Id
Wait-Process -Id $p.Id
`
	if err := os.WriteFile(parent, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	name, args := command(DefaultShells()[bundle.RunnerPowerShell], bundle.RunnerPowerShell, parent, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := RunLocal(ctx, dir, name, args)
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("timeout não interrompeu: %v", err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if err != nil {
		t.Fatalf("PID do filho: %q %v", out, err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return // processo já foi recolhido
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	if code == 259 { // STILL_ACTIVE
		_ = windows.TerminateProcess(h, 1)
		t.Fatal("o processo filho continuou rodando depois do timeout")
	}
}
