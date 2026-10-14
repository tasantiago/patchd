package hoststate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// loggedUsers lê as sessões do systemd-logind; sem ele (um container, uma distribuição sem
// systemd), o utmp. Sem nenhum dos dois, ninguém está conectado pelo que o sistema registra.
func loggedUsers(ctx context.Context) ([]protocol.LoggedUser, error) {
	gecos := func(u string) string { return names.get(u, func() string { return gecosName(u) }) }
	if us, ok := logindUsers(gecos); ok {
		return us, nil
	}
	var b []byte
	var err error
	for _, p := range []string{"/run/utmp", "/var/run/utmp"} {
		if b, err = os.ReadFile(p); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []protocol.LoggedUser{}, nil
	case err != nil:
		return nil, err
	}
	alive := func(pid int32) bool {
		_, err := os.Stat("/proc/" + strconv.Itoa(int(pid)))
		return err == nil
	}
	return utmpUsers(parseUtmp(b), alive, gecos), nil
}

// logindDir: onde o logind guarda as sessões.
const logindDir = "/run/systemd/sessions"

// logindUsers: ok=false quando o diretório não existe (o sistema não usa o logind).
func logindUsers(gecos func(string) string) ([]protocol.LoggedUser, bool) {
	entries, err := os.ReadDir(logindDir)
	if err != nil {
		return nil, false
	}
	var out []protocol.LoggedUser
	for _, e := range entries {
		// Os arquivos de sessão têm o ID como nome; os ".ref" são os canais de referência.
		if e.IsDir() || strings.Contains(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(logindDir + "/" + e.Name())
		if err != nil {
			continue // a sessão fechou entre a listagem e a leitura
		}
		if u, ok := logindSession(string(b)); ok {
			u.DisplayName = clip(gecos(u.Account))
			out = append(out, u)
		}
	}
	return tidyUsers(out), true
}

// gecosName: o nome completo do GECOS (a parte antes da primeira vírgula), quando a conta
// está no /etc/passwd. Contas do AD pelo sssd não estão: o nome fica vazio.
func gecosName(name string) string {
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	n, _, _ := strings.Cut(u.Name, ",")
	return strings.TrimSpace(n)
}
