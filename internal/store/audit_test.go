package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

type contratoAuditoria interface {
	RecordAudit(ctx context.Context, e AuditEvent) error
	AuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error)
}

// Aula 15.13: a trilha grava, filtra por usuário, ação (exata ou por prefixo) e período, e
// pagina pelo id.
func testarAuditoria(t *testing.T, st contratoAuditoria) {
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i, e := range []AuditEvent{
		{Username: "ana", Source: "ad", Action: "login.ok"},
		{Username: "ana", Source: "ad", Action: "patch.aprovar", Target: "CVE-2026-0001", Detail: "lab"},
		{Username: "bia", Source: "oidc", Action: "patch.aprovar_correcao", Target: "26300.9711", Detail: strings.Repeat("x", 5000)},
		{Username: "ana", Source: "ad", Action: "job.criar", Target: "maq-1", Remote: "192.0.2.10", RequestID: "abc"},
	} {
		e.At = base.Add(time.Duration(i) * 10 * time.Minute)
		if err := st.RecordAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	todos, err := st.AuditEvents(ctx, AuditFilter{})
	if err != nil || len(todos) != 4 || todos[0].Action != "job.criar" || todos[0].Remote != "192.0.2.10" || !todos[0].At.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("todos: %+v %v", todos, err)
	}
	if len(todos[1].Detail) != maxAuditDetail {
		t.Errorf("detalhe cortado em %d: %d", maxAuditDetail, len(todos[1].Detail))
	}
	conta := func(f AuditFilter) int {
		t.Helper()
		l, err := st.AuditEvents(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	for nome, c := range map[string]struct {
		f    AuditFilter
		quer int
	}{
		"usuário":           {AuditFilter{Username: "ana"}, 3},
		"ação exata":        {AuditFilter{Action: "patch.aprovar"}, 1},
		"prefixo":           {AuditFilter{Action: "patch."}, 2},
		"prefixo com _ e %": {AuditFilter{Action: "patch_%."}, 0},
		"período":           {AuditFilter{Since: base.Add(15 * time.Minute)}, 2},
		"página":            {AuditFilter{BeforeID: todos[1].ID}, 2},
		"limite":            {AuditFilter{Limit: 1}, 1},
		"tudo junto":        {AuditFilter{Username: "ana", Action: "patch.", Since: base}, 1},
	} {
		if n := conta(c.f); n != c.quer {
			t.Errorf("%s: %d, quer %d", nome, n, c.quer)
		}
	}
}

func TestAuditoriaMemoria(t *testing.T) { testarAuditoria(t, NewMemory()) }

func TestAuditoriaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAuditoria(t, NewPostgres(pool))
	// Só se acrescenta: UPDATE é recusado pelo gatilho.
	_, err := pool.Exec(context.Background(), `UPDATE audit_events SET username = 'outro'`)
	if err == nil || !strings.Contains(err.Error(), "só se acrescenta") {
		t.Errorf("UPDATE: %v", err)
	}
}

// TestAuditoriaComTextoInvalido (Aula 20.4b, achado F8): um pedido com %00 ou UTF-8 inválido
// no caminho tem de entrar na trilha, com o texto trocado por "�", no PostgreSQL e na memória.
func TestAuditoriaComTextoInvalido(t *testing.T) {
	testar := func(t *testing.T, st contratoAuditoria) {
		ctx := context.Background()
		e := AuditEvent{Username: "leitor\x00", Source: "local", Action: "acesso.negado",
			Target: "/painel/configuracao\x00", Detail: "perfil \xc3\x28leitura", Remote: "192.0.2.1", RequestID: "abc"}
		if err := st.RecordAudit(ctx, e); err != nil {
			t.Fatalf("gravar: %v", err)
		}
		l, err := st.AuditEvents(ctx, AuditFilter{Action: "acesso.negado"})
		if err != nil || len(l) != 1 || l[0].Target != "/painel/configuracao�" || l[0].Username != "leitor�" ||
			!strings.Contains(l[0].Detail, "�(leitura") {
			t.Errorf("%+v %v", l, err)
		}
	}
	t.Run("memoria", func(t *testing.T) { testar(t, NewMemory()) })
	t.Run("postgres", func(t *testing.T) {
		pool := bancoDeTeste(t)
		if err := Migrate(context.Background(), pool, silencioso()); err != nil {
			t.Fatal(err)
		}
		testar(t, NewPostgres(pool))
	})
}
