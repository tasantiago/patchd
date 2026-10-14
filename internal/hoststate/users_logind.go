package hoststate

import (
	"bufio"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O systemd-logind grava cada sessão em /run/systemd/sessions/<id>, em linhas CHAVE=valor
// (o que o loginctl mostra). As distribuições recentes deixaram de manter o utmp, por
// causa do ano 2038 (os campos de tempo dele são de 32 bits); o logind é a fonte delas.
//
// O arquivo começa com "This is private data. Do not parse.": a interface oficial é o
// D-Bus (org.freedesktop.login1), e o agente não tem cliente D-Bus. O formato é o mesmo
// há mais de dez anos; se mudar, as linhas que não casam viram "não é de usuário" e a
// lista sai vazia, sem derrubar a coleta. O risco é aceito e fica dito aqui.

// logindSession lê o arquivo de uma sessão. ok=false: não é de usuário (a tela de login,
// o gerenciador do usuário) ou está fechando.
func logindSession(content string) (protocol.LoggedUser, bool) {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = v
		}
	}
	if kv["USER"] == "" || kv["CLASS"] != "user" || kv["STATE"] == "closing" {
		return protocol.LoggedUser{}, false
	}
	u := protocol.LoggedUser{Account: clip(kv["USER"]), Active: kv["ACTIVE"] != "0"}
	service, tty := kv["SERVICE"], kv["TTY"]
	switch {
	case strings.Contains(service, "xrdp"):
		u.Session = protocol.SessionRDP
	case kv["REMOTE"] == "1" || strings.Contains(service, "sshd"):
		u.Session = protocol.SessionSSH
	case kv["TYPE"] == "x11" || kv["TYPE"] == "wayland" || kv["TYPE"] == "mir" || strings.HasPrefix(tty, "tty") || kv["SEAT"] != "":
		u.Session = protocol.SessionConsole
	default:
		u.Session = protocol.SessionTerminal
	}
	return u, true
}
