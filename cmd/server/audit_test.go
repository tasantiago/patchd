package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

func TestAuditCommand(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	agora := time.Now().UTC()
	for _, e := range []store.AuditEvent{
		{At: agora.Add(-48 * time.Hour), Username: "ana", Source: "ad", Action: "login.ok", Remote: "192.0.2.10", RequestID: "r1"},
		{At: agora.Add(-time.Hour), Username: "bia", Source: "oidc", Action: "patch.aprovar_correcao", Target: "26300.9711", Detail: "1006 aprovada(s)"},
	} {
		st.RecordAudit(ctx, e)
	}
	roda := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := auditCommand(ctx, st, args, agora, &out, &errb)
		return code, out.String() + errb.String()
	}
	code, out := roda()
	if code != exitOK || !strings.Contains(out, "Correção aprovada") || !strings.Contains(out, "26300.9711") || strings.Index(out, "bia") > strings.Index(out, "ana") {
		t.Errorf("tudo: %d\n%s", code, out)
	}
	// Sem IP nem request_id: a saída vai para chamados.
	if strings.Contains(out, "192.0.2.10") || strings.Contains(out, "r1") {
		t.Errorf("a saída expõe o IP ou o request_id:\n%s", out)
	}
	if _, out := roda("-since", "24h"); strings.Contains(out, "ana") || !strings.Contains(out, "bia") {
		t.Errorf("-since:\n%s", out)
	}
	if _, out := roda("-action", "login."); !strings.Contains(out, "Login") || strings.Contains(out, "bia") {
		t.Errorf("-action prefixo:\n%s", out)
	}
	if _, out := roda("-user", "ninguem"); !strings.Contains(out, "nenhum evento") {
		t.Errorf("vazio:\n%s", out)
	}
	if code, _ := roda("-n", "0"); code != exitConfig {
		t.Error("-n 0")
	}
}
