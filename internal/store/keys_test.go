package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func testarChaves(t *testing.T, st interface {
	SaveInventory(context.Context, string, protocol.InventoryReport) (bool, error)
	SaveHostState(context.Context, string, *protocol.MachineState, string) error
	KeyStats(context.Context, time.Time) (KeyStats, error)
}) {
	t.Helper()
	ctx := context.Background()
	agora := time.Now().UTC()
	for i, m := range []struct {
		id         string
		job, relea []string
	}{
		{"11111111-1111-1111-1111-111111111111", []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"}, []string{"cccccccccccc"}},
		{"22222222-2222-2222-2222-222222222222", []string{"aaaaaaaaaaaa"}, []string{"cccccccccccc", "dddddddddddd"}},
		{"33333333-3333-3333-3333-333333333333", nil, nil}, // agente antigo
	} {
		if _, err := st.SaveInventory(ctx, m.id, inv(string(rune('a'+i)), "1")); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveHostState(ctx, m.id, &protocol.MachineState{CollectedAt: agora, JobKeys: m.job, ReleaseKeys: m.relea}, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	ks, err := st.KeyStats(ctx, agora.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ks.Machines != 3 || ks.Reporting != 2 || ks.Job["aaaaaaaaaaaa"] != 2 || ks.Job["bbbbbbbbbbbb"] != 1 ||
		ks.Release["cccccccccccc"] != 2 || ks.Release["dddddddddddd"] != 1 {
		t.Errorf("%+v", ks)
	}
	if ks, _ := st.KeyStats(ctx, agora.Add(time.Hour)); ks.Machines != 0 || len(ks.Job) != 0 {
		t.Errorf("nada depois de since: %+v", ks)
	}
}

func TestChavesMemoria(t *testing.T) { testarChaves(t, NewMemory()) }

func TestChavesPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarChaves(t, NewPostgres(pool))
}
