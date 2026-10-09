package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/window"
)

type contratoAneis interface {
	contratoJobs
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	SetMachineRing(ctx context.Context, id string, ring int) (bool, error)
	MachineRing(ctx context.Context, id string) (int, bool, error)
	RingMachines(ctx context.Context, ring int) ([]string, error)
	SetWindow(ctx context.Context, ring int, w window.Window, by string) error
	DeleteWindow(ctx context.Context, ring int) (bool, error)
	Windows(ctx context.Context) ([]RingWindow, error)
}

func testarAneis(t *testing.T, st contratoAneis) {
	ctx := context.Background()
	for _, id := range []string{"anel-a", "anel-b", "anel-c"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Toda máquina nasce no anel 1; o piloto é o 0.
	if r, ok, _ := st.MachineRing(ctx, "anel-a"); !ok || r != DefaultRing {
		t.Errorf("anel padrão: %d %t", r, ok)
	}
	if ok, err := st.SetMachineRing(ctx, "anel-a", PilotRing); !ok || err != nil {
		t.Fatalf("piloto: %t %v", ok, err)
	}
	if _, err := st.SetMachineRing(ctx, "anel-b", 10); !errors.Is(err, ErrRing) {
		t.Errorf("anel 10: %v", err)
	}
	if ok, _ := st.SetMachineRing(ctx, "nao-existe", 0); ok {
		t.Error("máquina inexistente")
	}
	_, _ = st.RetireMachine(ctx, "anel-c", "teste", "t")
	if ok, _ := st.SetMachineRing(ctx, "anel-c", 0); ok {
		t.Error("aposentada não muda de anel")
	}
	if ids, _ := st.RingMachines(ctx, 0); len(ids) != 1 || ids[0] != "anel-a" {
		t.Errorf("anel 0: %v", ids)
	}
	ms, _ := st.Machines(ctx)
	aneis := map[string]int{}
	for _, m := range ms {
		aneis[m.ID] = m.Ring
	}
	if aneis["anel-a"] != 0 || aneis["anel-b"] != 1 {
		t.Errorf("resumo: %v", aneis)
	}

	// Janelas: grava, troca, lista com o fuso e remove.
	pvh, _ := time.LoadLocation("America/Porto_Velho")
	semana, _ := window.ParseDays("seg-sex")
	w := window.Window{Days: semana, Start: 12 * time.Hour, Duration: 2 * time.Hour, Location: pvh}
	if err := st.SetWindow(ctx, 0, w, "thyago"); err != nil {
		t.Fatal(err)
	}
	w.Start = 13 * time.Hour
	if err := st.SetWindow(ctx, 0, w, "outro"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWindow(ctx, 1, window.Window{Days: semana, Start: 0, Duration: time.Minute, Location: pvh}, "x"); err == nil {
		t.Error("janela de 1 minuto")
	}
	list, err := st.Windows(ctx)
	if err != nil || len(list) != 1 || list[0].Window.String() != "seg-sex 13:00-15:00 (America/Porto_Velho)" || list[0].UpdatedBy != "outro" {
		t.Fatalf("janelas: %+v %v", list, err)
	}
	if _, ok := WindowFor(list, 0); !ok {
		t.Error("WindowFor")
	}
	if ok, _ := st.DeleteWindow(ctx, 0); !ok {
		t.Error("remover")
	}
	if list, _ := st.Windows(ctx); len(list) != 0 {
		t.Error("removida")
	}

	// Job com janela: não é entregue antes do início; depois, sim.
	agora := time.Now().UTC().Truncate(time.Microsecond)
	inicio := agora.Add(time.Hour)
	id, _ := st.NextJobID(ctx)
	if err := st.CreateJob(ctx, NewJob{ID: id, MachineID: "anel-a", Type: "rescan", Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
		CreatedBy: "t", NotBefore: inicio, NotAfter: inicio.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if env, _ := st.DeliverJobs(ctx, "anel-a", agora); len(env) != 0 {
		t.Error("entregue antes da janela")
	}
	if env, _ := st.DeliverJobs(ctx, "anel-a", inicio.Add(time.Minute)); len(env) != 1 {
		t.Error("não entregue na janela")
	}
	if j, _, _ := st.JobForMachine(ctx, "anel-a", id); j.NotBefore == nil || !j.NotBefore.Equal(inicio) {
		t.Errorf("not_before: %+v", j.NotBefore)
	}
}

func TestAneisMemoria(t *testing.T) { testarAneis(t, NewMemory()) }

func TestAneisPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAneis(t, NewPostgres(pool))
}
