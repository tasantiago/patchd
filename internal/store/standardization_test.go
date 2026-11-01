package store

import (
	"context"
	"encoding/json"
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
	_, key, err := jobs.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	var newest int64
	for _, version := range []string{"1.0", "2.0"} {
		params := jobs.ThirdPartyParams{App: "7zip", Version: version, SHA256: strings.Repeat("a", 64), Size: 1, Installer: "exe", Args: []string{"/S"}}
		raw, _ := json.Marshal(params)
		rec, err := jobs.Issue(ctx, st, key, jobs.Request{MachineID: "pc", Type: jobs.TypeThirdParty, TTL: time.Hour, By: StandardizationAuthor, Params: raw}, now)
		if err != nil {
			t.Fatal(err)
		}
		newest = rec.ID
	}
	for _, sha := range []string{strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		params := jobs.ScriptParams{Bundle: "aplicativo", SHA256: sha, Size: 1, Entry: "install.sh", Runner: "bash", Timeout: 600}
		raw, _ := json.Marshal(params)
		if _, err := jobs.Issue(ctx, st, key, jobs.Request{MachineID: "pc", Type: jobs.TypeScript, TTL: time.Hour, By: StandardizationAuthor, Params: raw}, now); err != nil {
			t.Fatal(err)
		}
	}
	latest, open, err = st.StandardizationJobs(ctx, "pc", now)
	if err != nil || open != 4 || len(latest) != 3 || latest["7zip"].ID != newest {
		t.Fatalf("últimos jobs por aplicativo: %v abertos=%d err=%v", latest, open, err)
	}
	latest, open, err = st.StandardizationJobs(ctx, "pc", now.Add(2*time.Hour))
	if err != nil || open != 0 || latest["7zip"].State != jobs.StateExpired {
		t.Fatalf("expiração: %v abertos=%d err=%v", latest, open, err)
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
