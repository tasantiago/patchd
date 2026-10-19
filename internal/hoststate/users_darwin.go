package hoststate

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// loggedUsers no macOS (Aula 18.2): o who, o dono do /dev/console (quem está na tela) e o
// nome completo pelo id -F, que lê o diretório do sistema (contas locais e, num Mac no AD,
// as do domínio). Caminhos absolutos: o agente roda como root, pelo launchd.
func loggedUsers(ctx context.Context) ([]protocol.LoggedUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/who").Output()
	if err != nil {
		return nil, err
	}
	console := ""
	if b, err := exec.CommandContext(ctx, "/usr/bin/stat", "-f", "%Su", "/dev/console").Output(); err == nil {
		console = strings.TrimSpace(string(b))
		if console == "root" {
			console = "" // a tela de login: ninguém na tela
		}
	}
	full := func(u string) string {
		return names.get(u, func() string {
			if strings.HasPrefix(u, "-") { // viraria opção do id (Aula 20.2)
				return ""
			}
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// #nosec G204 -- programa fixo; u é um nome de conta do utmpx, sem "-" no começo
			b, err := exec.CommandContext(c, "/usr/bin/id", "-F", u).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		})
	}
	return whoUsers(parseWho(string(out)), console, full), nil
}
