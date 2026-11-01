package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

type standardStore interface {
	SaveInventory(context.Context, string, protocol.InventoryReport) (bool, error)
	StandardizationPolicies(context.Context) ([]StandardizationPolicy, error)
	SaveStandardizationPolicy(context.Context, StandardizationPolicy) (int64, error)
	DeleteStandardizationPolicy(context.Context, int64, int64) error
	WithStandardizationLock(context.Context, string, func(context.Context) error) (bool, error)
	SaveStandardizationState(context.Context, StandardizationState) error
	ReportStandardization(context.Context, string, string, protocol.StandardizationResult, time.Time) error
	StandardizationStates(context.Context) ([]StandardizationState, error)
	StandardizationJobs(context.Context, string, time.Time) (map[string]JobRow, int, error)
	jobs.Queue
	FinishJob(context.Context, int64, string, string, time.Time) (bool, error)
}

func testStandardizationStore(t *testing.T, st standardStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.SaveInventory(ctx, "pc", inv("standard", "1")); err != nil {
		t.Fatal(err)
	}
	p := StandardizationPolicy{Name: "Financeiro", Enabled: true, Machines: []string{"pc"}, Apps: []string{"7zip"}}
	id, err := st.SaveStandardizationPolicy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.StandardizationPolicies(ctx)
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Revision != 1 {
		t.Fatalf("políticas: %+v %v", list, err)
	}
	list[0].Apps[0] = "vscode"
	again, _ := st.StandardizationPolicies(ctx)
	if again[0].Apps[0] != "7zip" {
		t.Fatal("mutação vazou do armazenamento")
	}
	p = again[0]
	p.Description = "atualizado"
	if _, err := st.SaveStandardizationPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveStandardizationPolicy(ctx, p); !errors.Is(err, ErrStandardizationConflict) {
		t.Fatalf("gravação obsoleta: %v", err)
	}
	if err := st.DeleteStandardizationPolicy(ctx, id, 1); !errors.Is(err, ErrStandardizationConflict) {
		t.Fatalf("exclusão obsoleta: %v", err)
	}
	locked, err := st.WithStandardizationLock(ctx, "pc", func(ctx context.Context) error {
		other, err := st.WithStandardizationLock(ctx, "pc", func(context.Context) error { t.Error("executou avaliação concorrente"); return nil })
		if err != nil || other {
			t.Errorf("trava concorrente: %t %v", other, err)
		}
		return nil
	})
	if err != nil || !locked {
		t.Fatalf("trava: %t %v", locked, err)
	}
	now := time.Now().UTC()
	plan := protocol.StandardizationPlan{Revision: strings.Repeat("a", 64), Policies: []string{"Financeiro"}}
	state := StandardizationState{MachineID: "pc", Account: `CORP\Ana`, Plan: plan, EvaluatedAt: now}
	if err := st.SaveStandardizationState(ctx, state); err != nil {
		t.Fatal(err)
	}
	result := protocol.StandardizationResult{Revision: plan.Revision, Items: []protocol.StandardShortcutResult{{Name: "Portal", State: "aplicado"}}}
	if err := st.ReportStandardization(ctx, "pc", `corp\ana`, result, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveStandardizationState(ctx, state); err != nil {
		t.Fatal(err)
	}
	states, err := st.StandardizationStates(ctx)
	if err != nil || len(states) != 1 || states[0].ShortcutResult == nil {
		t.Fatalf("relato não preservado: %+v %v", states, err)
	}
	state.Plan.Revision = strings.Repeat("b", 64)
	if err := st.SaveStandardizationState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := st.ReportStandardization(ctx, "pc", `corp\ana`, result, now); !errors.Is(err, ErrStandardizationStale) {
		t.Fatalf("relato obsoleto: %v", err)
	}
	states, err = st.StandardizationStates(ctx)
	if err != nil || states[0].ShortcutResult != nil {
		t.Fatalf("relato da revisão anterior mantido: %+v %v", states, err)
	}
	if err := st.DeleteStandardizationPolicy(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	latest, open, err := st.StandardizationJobs(ctx, "pc", now)
	if err != nil || len(latest) != 0 || open != 0 {
		t.Fatalf("jobs vazios: %v %d %v", latest, open, err)
	}
}

func TestStandardizationStoreMemory(t *testing.T) { testStandardizationStore(t, NewMemory()) }
func TestStandardizationStorePostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testStandardizationStore(t, NewPostgres(pool))
}
