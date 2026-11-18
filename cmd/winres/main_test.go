package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWinres(t *testing.T) {
	dir := t.TempDir()
	var o, e bytes.Buffer
	if code := run([]string{"-version", "v0.17.13", "-name", "Central de software", "-file", "patchd-app.exe", "-out", dir}, &o, &e); code != 0 {
		t.Fatalf("%d %s", code, e.String())
	}
	for _, a := range []string{"amd64", "arm64"} {
		if fi, err := os.Stat(filepath.Join(dir, "versao_windows_"+a+".syso")); err != nil || fi.Size() < 200 {
			t.Errorf("%s: %v", a, err)
		}
	}
	for _, args := range [][]string{{"-out", dir}, {"-version", "v1.0.0", "-name", "", "-out", dir}, {"-version", "v1.0.0", "-name", "a\tb", "-out", dir},
		{"-version", "v1.0.0", "-file", `..\x.exe`, "-out", dir}, {"-version", "v1.0.0", "-file", "app", "-out", dir}} {
		if code := run(args, &o, &e); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

// Com -icon: o .ico da Central entra no mesmo objeto; um arquivo que não é .ico é recusado.
func TestWinresIcone(t *testing.T) {
	dir := t.TempDir()
	var o, e bytes.Buffer
	sem := []string{"-version", "v0.17.13", "-file", "patchd-app.exe", "-out", dir}
	if code := run(sem, &o, &e); code != 0 {
		t.Fatalf("%d %s", code, e.String())
	}
	semIcone, _ := os.Stat(filepath.Join(dir, "versao_windows_amd64.syso"))
	if code := run(append(sem, "-icon", filepath.Join("..", "app", "icone", "central-de-software.ico")), &o, &e); code != 0 {
		t.Fatalf("%d %s", code, e.String())
	}
	comIcone, _ := os.Stat(filepath.Join(dir, "versao_windows_amd64.syso"))
	if comIcone.Size() < semIcone.Size()+50000 {
		t.Errorf("o ícone não entrou: %d → %d bytes", semIcone.Size(), comIcone.Size())
	}
	ruim := filepath.Join(dir, "x.ico")
	_ = os.WriteFile(ruim, []byte("não é ícone"), 0o600)
	for _, f := range []string{ruim, filepath.Join(dir, "nao-existe.ico")} {
		if code := run(append(sem, "-icon", f), &o, &e); code != 2 {
			t.Errorf("%s: %d", f, code)
		}
	}
}
