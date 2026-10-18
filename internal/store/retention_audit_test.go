package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Aula 18.1: a retenção da trilha de auditoria — o que passou do prazo sai, e a limpeza
// entra na própria trilha; a simulação não apaga nem registra.
func TestRetencaoDaAuditoria(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	agora := time.Now().UTC()
	for _, idade := range []time.Duration{2000 * 24 * time.Hour, 1900 * 24 * time.Hour, 10 * 24 * time.Hour} {
		if err := st.RecordAudit(ctx, AuditEvent{At: agora.Add(-idade), Username: "thyago", Action: "patch.aprovar"}); err != nil {
			t.Fatal(err)
		}
	}
	conta := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&n)
		return n
	}
	r := Retention{History: 90 * 24 * time.Hour, Audit: 1825 * 24 * time.Hour}
	if got, err := st.Prune(ctx, r, agora, true); err != nil || got.Audit != 2 || conta() != 3 {
		t.Fatalf("simulação: %+v %v (sobraram %d)", got, err, conta())
	}
	if got, err := st.Prune(ctx, r, agora, false); err != nil || got.Audit != 2 {
		t.Fatalf("aplicar: %+v %v", got, err)
	}
	ev, _ := st.AuditEvents(ctx, AuditFilter{})
	if len(ev) != 2 || ev[0].Action != "auditoria.retencao" || ev[0].Username != "patchd-server" || ev[0].Source != "sistema" ||
		!strings.Contains(ev[0].Detail, "2 evento(s) anteriores a") || !strings.Contains(ev[0].Detail, "1825 dias") {
		t.Errorf("trilha depois da limpeza: %+v", ev)
	}
	// Nada mais a apagar: não registra de novo. Com 0, guarda sempre.
	if got, _ := st.Prune(ctx, r, agora, false); got.Audit != 0 || conta() != 2 {
		t.Errorf("segunda vez: %+v (%d)", got, conta())
	}
	if got, _ := st.Prune(ctx, Retention{History: 90 * 24 * time.Hour}, agora.Add(10000*24*time.Hour), false); got.Audit != 0 || conta() != 2 {
		t.Errorf("guardar sempre: %+v (%d)", got, conta())
	}
}
