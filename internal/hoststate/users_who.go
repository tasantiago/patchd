package hoststate

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O macOS (Aula 18.2): a saída do who, uma sessão por linha:
//
//	alice    console  Oct 16 09:00
//	alice    ttys000  Oct 16 09:05
//	bob      ttys001  Oct 16 10:12 (10.0.0.5)
//
// O utmpx do macOS tem outro formato que o do Linux (registros de outro tamanho, com um
// cabeçalho); o who é o leitor oficial dele, e a data, que pode vir no idioma do sistema,
// não é usada.

// whoEntry é uma linha do who.
type whoEntry struct {
	User, Line, Host string
}

// parseWho lê as linhas do who; linhas vazias ou estranhas ficam de fora.
func parseWho(out string) []whoEntry {
	var es []whoEntry
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		e := whoEntry{User: f[0], Line: f[1]}
		if t := strings.TrimSpace(l); strings.HasSuffix(t, ")") {
			if i := strings.LastIndex(t, "("); i >= 0 {
				e.Host = t[i+1 : len(t)-1]
			}
		}
		es = append(es, e)
	}
	return es
}

// whoUsers monta a lista: console é quem está na tela do Mac (com a troca rápida de
// usuários, só o dono do /dev/console está ativo; os outros ficam como desconectados); um
// terminal com host é ssh; sem host, um terminal local.
func whoUsers(es []whoEntry, consoleOwner string, display func(string) string) []protocol.LoggedUser {
	var out []protocol.LoggedUser
	for _, e := range es {
		u := protocol.LoggedUser{Account: clip(e.User), Active: true}
		switch {
		case e.Line == "console":
			u.Session = protocol.SessionConsole
			u.Active = consoleOwner == "" || strings.EqualFold(consoleOwner, e.User)
		case e.Host != "" && !strings.HasPrefix(e.Host, ":"):
			u.Session = protocol.SessionSSH
		default:
			u.Session = protocol.SessionTerminal
		}
		u.DisplayName = clip(display(e.User))
		out = append(out, u)
	}
	return tidyUsers(out)
}
