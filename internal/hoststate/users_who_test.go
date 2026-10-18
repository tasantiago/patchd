package hoststate

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 18.2: os usuários conectados no macOS, pela saída do who.
func TestUsuariosDoMac(t *testing.T) {
	out := `alice    console  Oct 16 09:00
alice    ttys000  Oct 16 09:05
alice    ttys001  Oct 16 09:07
bob      ttys002  out 16 10:12 (10.0.0.5)
carol    console  Oct 16 08:00

lixo
`
	nomes := map[string]string{"alice": "Alice Souza", "bob": "Bob Lima"}
	us := whoUsers(parseWho(out), "alice", func(u string) string { return nomes[u] })
	got := []string{}
	for _, u := range us {
		got = append(got, u.Account+"/"+u.Session+"/"+map[bool]string{true: "ativa", false: "desconectada"}[u.Active]+"/"+u.DisplayName)
	}
	quer := "alice/console/ativa/Alice Souza|bob/ssh/ativa/Bob Lima|alice/terminal/ativa/Alice Souza|carol/console/desconectada/"
	if strings.Join(got, "|") != quer {
		t.Errorf("usuários:\n%s\nquer\n%s", strings.Join(got, "|"), quer)
	}
	// Ninguém na tela (a tela de login é do root): os consoles contam como ativos — não dá para saber.
	if us := whoUsers(parseWho("alice console Oct 16 09:00\n"), "", func(string) string { return "" }); len(us) != 1 || !us[0].Active || us[0].Session != protocol.SessionConsole {
		t.Errorf("sem dono do console: %+v", us)
	}
	if us := whoUsers(parseWho(""), "alice", func(string) string { return "" }); us == nil || len(us) != 0 {
		t.Errorf("ninguém conectado é lista vazia, não nil (nil é \"não coletado\"): %#v", us)
	}
}
