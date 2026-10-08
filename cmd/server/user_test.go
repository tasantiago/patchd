package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

func TestComandosDeUsuario(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	roda := func(entrada string, args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := userCommand(ctx, st, args, strings.NewReader(entrada), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, _, e := roda("senha-do-administrador\n", "create", "-name", "Thyago", "-role", "admin"); code != exitOK || !strings.Contains(e, "thyago criado") {
		t.Fatalf("create: %d %s", code, e)
	}
	u, found, _ := st.PanelUser(ctx, "thyago")
	if !found || u.Role != panel.RoleAdmin || !panel.CheckPassword(u.PasswordHash, "senha-do-administrador") {
		t.Fatalf("usuário gravado: %+v", u)
	}
	// Recusas: perfil, nome, senha curta, sem senha, nome repetido.
	for _, c := range []struct {
		entrada string
		args    []string
		code    int
		msg     string
	}{
		{"senha-do-administrador\n", []string{"create", "-name", "x2", "-role", "root"}, exitConfig, "-role"},
		{"senha-do-administrador\n", []string{"create", "-name", "dominio\\x", "-role", "leitura"}, exitConfig, "-name"},
		{"curta\n", []string{"create", "-name", "x2", "-role", "leitura"}, exitConfig, "12 caracteres"},
		{"", []string{"create", "-name", "x2", "-role", "leitura"}, exitConfig, "nenhuma senha"},
		{"outra-senha-longa\n", []string{"create", "-name", "thyago", "-role", "leitura"}, exitRuntime, "já existe"},
		{"", []string{"remove"}, exitConfig, "desconhecido"},
	} {
		if code, _, e := roda(c.entrada, c.args...); code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}

	// \r\n (senha colada de um arquivo do Windows) é tirado; espaço nas pontas, recusado.
	if code, _, e := roda("senha-de-leitura-ok\r\n", "create", "-name", "consulta", "-role", "leitura"); code != exitOK {
		t.Fatalf("create com CRLF: %s", e)
	}
	if u, _, _ := st.PanelUser(ctx, "consulta"); !panel.CheckPassword(u.PasswordHash, "senha-de-leitura-ok") {
		t.Error("o \\r não faz parte da senha")
	}

	code, out, _ := roda("", "list")
	if code != exitOK || !strings.Contains(out, "consulta  leitura") || !strings.Contains(out, "thyago    admin") || strings.Contains(out, "pbkdf2") {
		t.Errorf("list: %d\n%s", code, out)
	}

	if code, _, e := roda("senha-nova-do-admin\n", "password", "-name", "thyago"); code != exitOK || !strings.Contains(e, "encerradas") {
		t.Errorf("password: %d %s", code, e)
	}
	if u, _, _ := st.PanelUser(ctx, "thyago"); !panel.CheckPassword(u.PasswordHash, "senha-nova-do-admin") {
		t.Error("senha não trocada")
	}
	if code, _, _ := roda("senha-nova-do-admin\n", "password", "-name", "ninguem"); code != exitRuntime {
		t.Error("password de usuário inexistente")
	}

	if code, _, e := roda("", "delete", "-name", "thyago"); code != exitOK || !strings.Contains(e, "nenhum administrador local") {
		t.Errorf("delete do último admin avisa: %d %s", code, e)
	}
	if code, _, _ := roda("", "delete", "-name", "thyago"); code != exitRuntime {
		t.Error("delete de novo")
	}
}

func TestSenhaNaoVemDoTerminal(t *testing.T) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skip("sem terminal neste ambiente")
	}
	defer tty.Close()
	if _, err := readPassword(tty); err == nil || !strings.Contains(err.Error(), "não do terminal") {
		t.Errorf("terminal: %v", err)
	}
}
