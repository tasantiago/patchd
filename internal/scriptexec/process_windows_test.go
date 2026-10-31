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

func TestWindowsEncerraFilhosNoCancelamento(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "filho.ps1")
	if err := os.WriteFile(child, []byte("Start-Sleep -Seconds 120"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "pai.ps1")
	script := `$p = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList @('-NoProfile', '-File', ('"' + (Join-Path $PSScriptRoot 'filho.ps1') + '"')) -PassThru
[System.IO.File]::WriteAllText((Join-Path $PSScriptRoot 'filho.pid'), [string]$p.Id)
Wait-Process -Id $p.Id
`
	if err := os.WriteFile(parent, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	name, args := command(DefaultShells()[bundle.RunnerPowerShell], bundle.RunnerPowerShell, parent, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := RunLocal(ctx, dir, name, args)
		done <- err
	}()
	// PowerShell pode levar mais de 5 s para iniciar em um runner ocupado.
	// Só cancela depois de observar um filho vivo, evitando um falso positivo.
	var pid uint64
	readyBy := time.Now().Add(30 * time.Second)
	for pid == 0 {
		if raw, err := os.ReadFile(filepath.Join(dir, "filho.pid")); err == nil {
			pid, _ = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
		}
		if pid != 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("o script terminou antes de iniciar o filho: %v", err)
		default:
		}
		if time.Now().After(readyBy) {
			t.Fatal("o filho não iniciou em 30 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("abrir o filho para conferir o encerramento: %v", err)
	}
	defer windows.CloseHandle(h)
	defer windows.TerminateProcess(h, 1)
	cancel()
	select {
	case err := <-done:
		if err == nil || ctx.Err() != context.Canceled {
			t.Fatalf("cancelamento não interrompeu: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a execução não terminou após o cancelamento")
	}
	state, err := windows.WaitForSingleObject(h, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("o filho continuou vivo: estado %d, erro %v", state, err)
	}
}
