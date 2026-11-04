package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoPredios interface {
	contratoAneis
	BuildingRings(ctx context.Context) ([]BuildingRing, error)
	SetBuildingRing(ctx context.Context, b BuildingRing) error
	DeleteBuildingRing(ctx context.Context, building string) (bool, error)
	RingPins(ctx context.Context) (map[string]bool, error)
	ApplyRings(ctx context.Context, target map[string]int) (int, error)
	UnpinRings(ctx context.Context, ids []string) (int, error)
}

func testarPredios(t *testing.T, st contratoPredios) {
	ctx := context.Background()
	for _, id := range []string{"pr-a", "pr-b", "pr-c"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetBuildingRing(ctx, BuildingRing{Building: "Sede", Ring: 3, UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBuildingRing(ctx, BuildingRing{Building: "Sede", Ring: 4, UpdatedBy: "bia"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBuildingRing(ctx, BuildingRing{Building: "", Ring: 1, UpdatedBy: "x"}); !errors.Is(err, ErrBuilding) {
		t.Errorf("prédio vazio: %v", err)
	}
	if err := st.SetBuildingRing(ctx, BuildingRing{Building: strings.Repeat("é", 201), Ring: 1, UpdatedBy: "x"}); !errors.Is(err, ErrBuilding) {
		t.Errorf("prédio longo: %v", err)
	}
	if err := st.SetBuildingRing(ctx, BuildingRing{Building: "Anexo", Ring: 10, UpdatedBy: "x"}); !errors.Is(err, ErrRing) {
		t.Errorf("grupo 10: %v", err)
	}
	if l, _ := st.BuildingRings(ctx); len(l) != 1 || l[0].Ring != 4 || l[0].UpdatedBy != "bia" || l[0].UpdatedAt.IsZero() {
		t.Errorf("vínculos: %+v", l)
	}

	// pr-b movida à mão: ApplyRings não a mexe; pr-a e pr-c mudam; a que já está não conta.
	if _, err := st.SetMachineRing(ctx, "pr-b", 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.RingPins(ctx); len(p) != 1 || !p["pr-b"] {
		t.Errorf("exceções: %v", p)
	}
	n, err := st.ApplyRings(ctx, map[string]int{"pr-a": 4, "pr-b": 4, "pr-c": 1, "nao-existe": 4})
	if err != nil || n != 1 {
		t.Errorf("aplicar: %d %v", n, err)
	}
	if r, _, _ := st.MachineRing(ctx, "pr-b"); r != 0 {
		t.Errorf("exceção mexida: %d", r)
	}
	if _, err := st.ApplyRings(ctx, map[string]int{"pr-a": 11}); !errors.Is(err, ErrRing) {
		t.Errorf("aplicar 11: %v", err)
	}
	// Aplicar não marca como movida à mão.
	if p, _ := st.RingPins(ctx); len(p) != 1 {
		t.Errorf("exceções depois de aplicar: %v", p)
	}
	if n, _ := st.UnpinRings(ctx, []string{"pr-a", "pr-b"}); n != 1 {
		t.Errorf("devolver: %d", n)
	}
	if n, _ := st.ApplyRings(ctx, map[string]int{"pr-b": 4}); n != 1 {
		t.Errorf("aplicar depois de devolver: %d", n)
	}
	if ok, _ := st.DeleteBuildingRing(ctx, "Sede"); !ok {
		t.Error("remover")
	}
	if ok, _ := st.DeleteBuildingRing(ctx, "Sede"); ok {
		t.Error("remover de novo")
	}
	if r, _, _ := st.MachineRing(ctx, "pr-a"); r != 4 {
		t.Errorf("remover moveu: %d", r)
	}
}

func TestPrediosMemoria(t *testing.T) { testarPredios(t, NewMemory()) }

func TestPrediosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarPredios(t, NewPostgres(pool))
}
