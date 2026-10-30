package biospw

import (
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/machinebox"
)

func TestCopiaDeEmergencia(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	pw, _ := Generate()
	const maq = "60ef97e4-0001"
	env, _ := Seal(pub, maq, pw)
	c := Copy{MachineID: maq, Guard: 12, KeyID: machinebox.KeyID(pub), Escrow: env}
	s, err := c.Format()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, CopyPrefix+":"+maq+":12:") || strings.Contains(s, pw) || strings.ContainsAny(s, " \n") {
		t.Fatalf("formato: %q", s)
	}
	lida, err := ParseCopy("  " + s + "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := lida.Reveal(priv); err != nil || got != pw {
		t.Fatalf("revelar: %q %v", got, err)
	}
	// Outra chave: diz qual era, sem tentar abrir.
	outra, _ := machinebox.Generate()
	if _, err := lida.Reveal(outra); !errors.Is(err, ErrOtherKey) {
		t.Errorf("outra chave: %v", err)
	}
	// Trocar a máquina na linha não engana: o envelope é preso ao ID.
	troca := strings.Replace(s, maq, "60ef97e4-0002", 1)
	if c2, err := ParseCopy(troca); err != nil {
		t.Fatal(err)
	} else if _, err := c2.Reveal(priv); !errors.Is(err, ErrOtherMachine) {
		t.Errorf("máquina trocada: %v", err)
	}
	for _, ruim := range []string{"", "patchd-bios-2:" + s[len(CopyPrefix)+1:], strings.Replace(s, ":12:", ":0:", 1),
		strings.Replace(s, ":12:", ":x:", 1), s + ":extra", s[:len(s)-3] + "!!!", CopyPrefix + "::1:" + c.KeyID + ":AAAA",
		strings.Repeat("a", 3000)} {
		if _, err := ParseCopy(ruim); err == nil {
			t.Errorf("aceita: %q", ruim)
		}
	}
	if _, err := (Copy{MachineID: "a:b", Guard: 1, KeyID: c.KeyID, Escrow: env}).Format(); err == nil {
		t.Error("ID com dois-pontos aceito")
	}
}
