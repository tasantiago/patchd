package main

import (
	"bytes"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Aula 17.4: a cópia do app é o mesmo executável, marcado como programa gráfico.
func TestGuiImage(t *testing.T) {
	if _, err := guiImage([]byte("não é exe")); err == nil {
		t.Error("lixo")
	}
	exe := filepath.Join(t.TempDir(), "a.exe")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("sem o build do Windows aqui: %v %s", err, out)
	}
	orig, _ := os.ReadFile(exe)
	gui, err := guiImage(orig)
	if err != nil {
		t.Fatal(err)
	}
	sub := func(b []byte) uint16 {
		f, err := pe.NewFile(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return f.OptionalHeader.(*pe.OptionalHeader64).Subsystem
	}
	if sub(orig) != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI || sub(gui) != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		t.Errorf("subsistema: %d → %d", sub(orig), sub(gui))
	}
	diff := 0
	for i := range orig {
		if orig[i] != gui[i] {
			diff++
		}
	}
	if len(gui) != len(orig) || diff != 1 {
		t.Errorf("só o campo muda: %d bytes diferentes", diff)
	}
}
