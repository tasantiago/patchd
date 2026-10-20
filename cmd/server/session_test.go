package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

func TestSessaoDeVarredura(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	agora := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	roda := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := sessionCommand(ctx, st, args, agora, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	code, out, e := roda("create")
	valor := strings.TrimSpace(out)
	if code != exitOK || !strings.HasPrefix(valor, panel.SessionPrefix) || strings.Count(out, "\n") != 1 || !strings.Contains(e, "perfil leitura") {
		t.Fatalf("create: %d %q %s", code, out, e)
	}
	s, found, err := st.PanelSession(ctx, identity.Hash(valor))
	if err != nil || !found || s.Role != panel.RoleRead || s.Source != panel.SourceScan || s.Username != "varredura" ||
		!s.ExpiresAt.Equal(agora.Add(2*time.Hour)) {
		t.Fatalf("sessão gravada: %+v %v %v", s, found, err)
	}
	if code, _, e := roda("create", "-role", "admin", "-hours", "1", "-name", "zap-teste"); code != exitOK || !strings.Contains(e, "atenção: perfil admin") {
		t.Errorf("admin: %d %s", code, e)
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"create", "-role", "root"}, "-role"},
		{[]string{"create", "-hours", "9"}, "-hours"},
		{[]string{"create", "-hours", "0"}, "-hours"},
		{[]string{"create", "-name", "dominio\\x"}, "-name"},
		{[]string{"apagar"}, "desconhecido"},
	} {
		if code, out, e := roda(c.args...); code != exitConfig || out != "" || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %q %s", c.args, code, out, e)
		}
	}
	if code, _, e := roda("end"); code != exitOK || !strings.Contains(e, "1 sessão(ões) de varredura") {
		t.Errorf("end: %d %s", code, e)
	}
	if _, found, _ := st.PanelSession(ctx, identity.Hash(valor)); found {
		t.Error("a sessão continuou depois do end")
	}
}
