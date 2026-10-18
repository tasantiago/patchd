package store

import (
	"context"
	"testing"
	"time"
)

type contratoAvisos interface {
	NotificationsSent(ctx context.Context, keys []string) (map[string]bool, error)
	MarkNotificationsSent(ctx context.Context, keys []string, at time.Time) error
	PruneNotifications(ctx context.Context, before time.Time) (int64, error)
}

// Aula 18.6: as chaves dos avisos mandados.
func testarAvisos(t *testing.T, st contratoAvisos) {
	ctx := context.Background()
	agora := time.Now().UTC().Truncate(time.Second)
	if m, err := st.NotificationsSent(ctx, nil); err != nil || len(m) != 0 {
		t.Fatalf("vazio: %v %v", m, err)
	}
	if err := st.MarkNotificationsSent(ctx, []string{"pedido:1", "pedido:2"}, agora.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Marcar de novo não muda a data (a chave é do primeiro envio).
	if err := st.MarkNotificationsSent(ctx, []string{"pedido:2", "campanha:3:99"}, agora); err != nil {
		t.Fatal(err)
	}
	m, err := st.NotificationsSent(ctx, []string{"pedido:1", "pedido:2", "pedido:3", "campanha:3:99"})
	if err != nil || len(m) != 3 || m["pedido:3"] {
		t.Fatalf("mandadas: %v %v", m, err)
	}
	if n, err := st.PruneNotifications(ctx, agora.Add(-90*24*time.Hour)); err != nil || n != 2 {
		t.Errorf("limpeza: %d %v", n, err)
	}
	if m, _ := st.NotificationsSent(ctx, []string{"pedido:1", "campanha:3:99"}); len(m) != 1 || !m["campanha:3:99"] {
		t.Errorf("depois da limpeza: %v", m)
	}
}

func TestAvisosMemoria(t *testing.T) { testarAvisos(t, NewMemory()) }

func TestAvisosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAvisos(t, NewPostgres(pool))
}
