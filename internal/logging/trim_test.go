package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Trim: acima do limite, o conteúdo vai para .1 (o anterior sai) e o arquivo zera; quem
// escreve com O_APPEND continua no mesmo arquivo, do começo.
func TestTrim(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update.log")
	if ok, err := Trim(p, 10); ok || err != nil {
		t.Fatalf("sem arquivo: %v %v", ok, err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString("curto\n")
	if ok, _ := Trim(p, 100); ok {
		t.Error("abaixo do limite")
	}
	_ = os.WriteFile(p+".1", []byte("o anterior"), 0o600)
	_, _ = f.WriteString(strings.Repeat("x", 200) + "\n")
	if ok, err := Trim(p, 100); !ok || err != nil {
		t.Fatalf("acima: %v %v", ok, err)
	}
	_, _ = f.WriteString("depois\n")
	velho, _ := os.ReadFile(p + ".1")
	novo, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(velho), "curto\n") || strings.Contains(string(velho), "o anterior") || string(novo) != "depois\n" {
		t.Errorf(".1 = %q; atual = %q", velho, novo)
	}
}
