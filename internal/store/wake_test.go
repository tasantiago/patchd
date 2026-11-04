package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoLigar interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (bool, error)
	SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	WakeRelayHosts(ctx context.Context, exclude string, since time.Time) ([]WakeHost, error)
	CreateWakeRequest(ctx context.Context, r WakeRequest) (int64, error)
	WakeRequests(ctx context.Context, machineID string, limit int) ([]WakeRequest, error)
	MarkWakeSent(ctx context.Context, jobID int64, relay string, at time.Time) (bool, error)
	MarkWakeAnswered(ctx context.Context, machineID string, at time.Time) error
}

func testarLigar(t *testing.T, st contratoLigar) {
	ctx := context.Background()
	for _, id := range []string{"alvo", "rep-1", "rep-2", "sem-estado", "aposentada"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id, OS: protocol.OSInfo{Hostname: "pc-" + id}}); err != nil {
			t.Fatal(err)
		}
		if id != "sem-estado" {
			s := &protocol.MachineState{WakeRelay: true, Interfaces: []protocol.NetInterface{{Name: "eth0", IPv4: []string{"192.0.2.9/24"}, Up: true}}}
			if err := st.SaveHostState(ctx, id, s, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.RetireMachine(ctx, "aposentada", "teste de aposentadoria", "t"); err != nil {
		t.Fatal(err)
	}
	hosts, err := st.WakeRelayHosts(ctx, "alvo", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, h := range hosts {
		ids[h.ID] = true
		if h.State == nil || !h.State.WakeRelay || h.Name != "pc-"+h.ID {
			t.Errorf("candidato %+v", h)
		}
	}
	if len(hosts) != 2 || !ids["rep-1"] || !ids["rep-2"] {
		t.Fatalf("candidatos %v", ids)
	}
	if hosts, _ := st.WakeRelayHosts(ctx, "alvo", time.Now().Add(time.Hour)); len(hosts) != 0 {
		t.Fatal("check-in velho entrou")
	}

	t0 := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	id, err := st.CreateWakeRequest(ctx, WakeRequest{MachineID: "alvo", RequestedAt: t0, RequestedBy: "thyago", Network: "192.0.2.0/24",
		MACs: []string{"00:11:22:33:44:55"}, Relays: []string{"rep-1", "rep-2"}, JobIDs: []int64{41, 42}})
	if err != nil || id <= 0 {
		t.Fatal(id, err)
	}
	if err := st.MarkWakeAnswered(ctx, "alvo", t0.Add(time.Minute)); err != nil { // antes do pacote: nada
		t.Fatal(err)
	}
	if ok, err := st.MarkWakeSent(ctx, 42, "rep-2", t0.Add(2*time.Minute)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := st.MarkWakeSent(ctx, 41, "rep-1", t0.Add(3*time.Minute)); ok {
		t.Fatal("o segundo repetidor trocou o primeiro")
	}
	if ok, _ := st.MarkWakeSent(ctx, 99, "rep-1", t0); ok {
		t.Fatal("job de outro pedido")
	}
	if err := st.MarkWakeAnswered(ctx, "alvo", t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = st.MarkWakeAnswered(ctx, "alvo", t0.Add(9*time.Minute)) // o segundo check-in não muda a resposta
	list, err := st.WakeRequests(ctx, "alvo", 5)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	r := list[0]
	if r.SentBy != "rep-2" || !r.SentAt.Equal(t0.Add(2*time.Minute)) || r.AnsweredAt == nil || !r.AnsweredAt.Equal(t0.Add(5*time.Minute)) ||
		len(r.JobIDs) != 2 || r.MACs[0] != "00:11:22:33:44:55" || r.Relays[1] != "rep-2" {
		t.Fatalf("pedido %+v", r)
	}
	// Um check-in muito depois do pacote não conta.
	id2, _ := st.CreateWakeRequest(ctx, WakeRequest{MachineID: "alvo", RequestedAt: t0.Add(time.Hour), RequestedBy: "t", Network: "192.0.2.0/24",
		MACs: []string{"00:11:22:33:44:55"}, Relays: []string{"rep-1"}, JobIDs: []int64{50}})
	_, _ = st.MarkWakeSent(ctx, 50, "rep-1", t0.Add(time.Hour))
	_ = st.MarkWakeAnswered(ctx, "alvo", t0.Add(time.Hour+WakeAnswerWindow+time.Minute))
	list, _ = st.WakeRequests(ctx, "alvo", 5)
	if len(list) != 2 || list[0].ID != id2 || list[0].AnsweredAt != nil {
		t.Fatalf("resposta fora da janela: %+v", list[0])
	}
}

func TestLigarMemoria(t *testing.T) { testarLigar(t, NewMemory()) }

func TestLigarPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarLigar(t, NewPostgres(pool))
}
