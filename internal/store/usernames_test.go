package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoNomesUsuarios interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (bool, error)
	SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error
	UserDisplayNames(ctx context.Context) (map[string]string, error)
}

// O nome de exibição de cada conta conectada, pela chave em minúsculas; sem nome, fica de fora.
func testarNomesUsuarios(t *testing.T, st contratoNomesUsuarios) {
	ctx := context.Background()
	for _, id := range []string{"u-1", "u-2"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, CollectedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SaveHostState(ctx, "u-1", &protocol.MachineState{Users: []protocol.LoggedUser{
		{Account: `EXEMPLO\ana.souza`, DisplayName: "Ana Souza", Session: protocol.SessionConsole, Active: true},
		{Account: `EXEMPLO\sem.nome`, Session: protocol.SessionRDP}}}, "")
	_ = st.SaveHostState(ctx, "u-2", &protocol.MachineState{}, "")
	m, err := st.UserDisplayNames(ctx)
	if err != nil || len(m) != 1 || m[`exemplo\ana.souza`] != "Ana Souza" {
		t.Fatalf("nomes: %v %v", m, err)
	}
}

func TestNomesUsuariosMemoria(t *testing.T) { testarNomesUsuarios(t, NewMemory()) }

func TestNomesUsuariosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarNomesUsuarios(t, NewPostgres(pool))
}
