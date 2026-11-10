package campaign_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Ligar pela rede na janela (Aula 22.3): minutos antes da janela, o pedido de ligar sai só para
// as desligadas (sem check-in recente), uma vez; quando os jobs terminam, o desligar vai para a
// que acordou, e a que não acordou fica anotada.
func TestLigarNaJanelaEDesligarDepois(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	base := a.agora
	relogio := base.Add(-3 * time.Hour)
	a.st.SetClock(func() time.Time { return relogio })
	for _, m := range []string{"a1", "a2", "a3"} {
		relogio = base.Add(-3 * time.Hour)
		if m == "a2" {
			relogio = base.Add(15 * time.Minute) // a2 ligada perto da janela
		}
		if _, err := a.st.SaveInventory(ctx, m, protocol.InventoryReport{SchemaVersion: 1, Hash: m + "-2"}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.st.SetMachineRing(ctx, m, 1); !ok {
			t.Fatal("anel")
		}
	}
	inicio := base.Add(30 * time.Minute)
	a.e.Window = func(context.Context, string, time.Time) (time.Time, time.Time, bool, error) {
		return inicio, inicio.Add(3 * time.Hour), true, nil
	}
	var pedidos []string
	a.e.Wake = func(ctx context.Context, id, by string) (int64, string, error) {
		pedidos = append(pedidos, id)
		r := store.WakeRequest{MachineID: id, RequestedAt: base, RequestedBy: by, Network: "192.0.2.0/24", MACs: []string{"00:11:22:33:44:55"},
			Relays: []string{"p1"}, JobIDs: []int64{1}}
		if id == "a1" {
			at := base.Add(25 * time.Minute)
			r.SentAt, r.AnsweredAt = &at, &at
		}
		rid, err := a.st.CreateWakeRequest(ctx, r)
		return rid, "", err
	}
	a.e.CanShutdown = func(context.Context, string) (bool, error) { return true, nil }

	c := campaign.Campaign{Name: "noite", Type: jobs.TypeRescan, Rings: []int{1}, UseWindow: true, Observe: time.Hour, TTL: time.Hour, CreatedBy: "t", Wake: true}
	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{1}, TTL: time.Hour, Wake: true}).Validate(); err == nil {
		t.Error("ligar pela rede sem janela aceito")
	}
	id := a.cria(c)
	a.passo(id, 0)
	if len(pedidos) != 0 {
		t.Fatalf("pediu antes da hora: %v", pedidos)
	}
	a.passo(id, 21*time.Minute)
	if strings.Join(pedidos, ",") != "a1,a3" {
		t.Fatalf("pedidos: %v (a2 está ligada)", pedidos)
	}
	a.passo(id, 22*time.Minute)
	if len(pedidos) != 2 {
		t.Fatalf("pediu de novo: %v", pedidos)
	}
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	for _, j := range l {
		estado := jobs.StateDone
		if j.MachineID == "a3" {
			estado = jobs.StateExpired
		}
		if ok, _ := a.st.FinishJob(ctx, j.ID, estado, "t", base.Add(40*time.Minute)); !ok {
			t.Fatal("encerrar")
		}
	}
	a.passo(id, 50*time.Minute)
	ws, _ := a.st.CampaignWakes(ctx, id, -1)
	if len(ws) != 2 {
		t.Fatalf("registros: %+v", ws)
	}
	for _, w := range ws {
		switch w.MachineID {
		case "a1":
			if w.ShutdownJobID == 0 || w.AnsweredAt == nil {
				t.Errorf("a1 sem o desligar: %+v", w)
			}
		case "a3":
			if w.ShutdownJobID != 0 || !strings.Contains(w.ShutdownNote, "não acordou") {
				t.Errorf("a3: %+v", w)
			}
		}
	}
	js, _ := a.st.Jobs(ctx, "a1", 10)
	if !slices.ContainsFunc(js, func(j store.JobRow) bool { return j.Type == jobs.TypeShutdown }) {
		t.Error("o job desligar não saiu para a1")
	}
	if js, _ := a.st.Jobs(ctx, "a2", 10); slices.ContainsFunc(js, func(j store.JobRow) bool { return j.Type == jobs.TypeShutdown }) {
		t.Error("desligou quem já estava ligada")
	}
	// Um passo depois: nada de novo.
	a.passo(id, 55*time.Minute)
	if js, _ := a.st.Jobs(ctx, "a1", 10); len(slices.DeleteFunc(js, func(j store.JobRow) bool { return j.Type != jobs.TypeShutdown })) != 1 {
		t.Error("desligar repetido")
	}
}
