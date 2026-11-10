package bioslegacy

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestRedactTiraSoAVersaoDoMarcador(t *testing.T) {
	const uninstall = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	list := []protocol.Software{
		{Name: "BIOS_password", Version: "Exemplo@12345678", Source: uninstall + `\BIOS_password`},
		{Name: "bios_PASSWORD", Version: "x", Source: uninstall + `\outra`},
		{Name: "renomeado", Version: "y", Source: uninstall + `\bios_password`},
		{Name: "Gerenciador de Password", Version: "2.1", Source: uninstall + `\{GUID}`},
		{Name: "HP BIOS Config", Version: "4.0", Source: uninstall + `\HPBCU`},
		{Name: "BIOS_password", Source: uninstall + `\BIOS_password`},
	}
	if n := Redact(list); n != 3 {
		t.Fatalf("mudou %d entradas, queria 3", n)
	}
	for i, want := range []string{"", "", "", "2.1", "4.0", ""} {
		if list[i].Version != want {
			t.Errorf("entrada %d (%s): versão %q, queria %q", i, list[i].Name, list[i].Version, want)
		}
	}
}

func TestInPath(t *testing.T) {
	for path, want := range map[string]bool{
		`HKLM\` + Path: true,
		`HKLM\SOFTWARE\Exemplo\bios_password\Historico\20260101_x`:                true,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\BIOS_passwords`: false,
		`HKLM\SOFTWARE\Policies\PasswordPolicy`:                                   false,
	} {
		if got := InPath(path); got != want {
			t.Errorf("InPath(%q) = %v, queria %v", path, got, want)
		}
	}
}

func TestUsable(t *testing.T) {
	for pw, want := range map[string]bool{
		"Exemplo@12345678":      true,
		"":                      false,
		strings.Repeat("a", 32): true,
		strings.Repeat("a", 33): false,
		"com\tcontrole":         false,
		"acentuação":            false,
	} {
		if got := Usable([]byte(pw)); got != want {
			t.Errorf("Usable(%q) = %v, queria %v", pw, got, want)
		}
	}
}

func TestMentions(t *testing.T) {
	if !Mentions("Outro 1.0; bios_password Exemplo@1") || Mentions("HP BIOS Config 4.0") {
		t.Error("Mentions errou")
	}
}
