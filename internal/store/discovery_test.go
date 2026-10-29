package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

type contratoDescoberta interface {
	AddDiscoveryRange(ctx context.Context, spec, note, by string) (int64, error)
	SetDiscoveryRangeEnabled(ctx context.Context, id int64, enabled bool) (bool, error)
	DeleteDiscoveryRange(ctx context.Context, id int64) (bool, error)
	DiscoveryRanges(ctx context.Context) ([]DiscoveryRange, error)
	ReplaceDiscoveryHosts(ctx context.Context, hosts []DiscoveryHost, run DiscoveryRun) (int64, error)
	DiscoveryHosts(ctx context.Context) ([]DiscoveryHost, error)
	DiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRun, error)
}

// Aula 23.7: faixas (cadastro, ligar/desligar, remover, repetida recusada) e a troca dos
// hosts a cada varredura, preservando o first_seen_at de quem continua vivo.
func testarDescoberta(t *testing.T, st contratoDescoberta) {
	ctx := context.Background()
	if r, _ := st.DiscoveryRanges(ctx); len(r) != 0 {
		t.Fatalf("faixas iniciais: %+v", r)
	}
	id, err := st.AddDiscoveryRange(ctx, "10.0.0.0/24", "prédio A", "thyago")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDiscoveryRange(ctx, "10.0.0.0/24", "", "thyago"); !errors.Is(err, ErrRangeExists) {
		t.Fatalf("repetida: %v", err)
	}
	id2, _ := st.AddDiscoveryRange(ctx, "10.0.1.0/24", "prédio B", "thyago")
	if ok, _ := st.SetDiscoveryRangeEnabled(ctx, id2, false); !ok {
		t.Fatal("desligar")
	}
	rs, _ := st.DiscoveryRanges(ctx)
	if len(rs) != 2 || rs[0].ID != id || !rs[0].Enabled || rs[0].Spec != "10.0.0.0/24" || rs[0].CreatedBy != "thyago" || rs[1].Enabled {
		t.Fatalf("faixas: %+v", rs)
	}
	if ok, _ := st.DeleteDiscoveryRange(ctx, id2); !ok {
		t.Fatal("remover")
	}
	if ok, _ := st.DeleteDiscoveryRange(ctx, 9999); ok {
		t.Fatal("remover inexistente não devia dar ok")
	}

	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fin := t0.Add(5 * time.Second)
	hosts := []DiscoveryHost{
		{IP: "10.0.0.22", Ports: "22", OSGuess: "ssh"},
		{IP: "10.0.0.10", Ports: "445, 3389", OSGuess: "windows", MatchID: "m-1", MatchKind: "ip"},
	}
	runID, err := st.ReplaceDiscoveryHosts(ctx, hosts, DiscoveryRun{StartedAt: t0, FinishedAt: &fin, Scanned: 256, Alive: 2, WithoutAgent: 1, StartedBy: "thyago"})
	if err != nil || runID == 0 {
		t.Fatalf("primeira varredura: %v", err)
	}
	hs, _ := st.DiscoveryHosts(ctx)
	if len(hs) != 2 || hs[0].IP != "10.0.0.10" || hs[1].IP != "10.0.0.22" { // ordenado pelo IP
		t.Fatalf("hosts: %+v", hs)
	}
	if hs[0].MatchID != "m-1" || hs[0].MatchKind != "ip" || hs[1].MatchID != "" || !hs[0].FirstSeenAt.Equal(t0) {
		t.Fatalf("host 0: %+v", hs[0])
	}

	// Segunda varredura, mais tarde: .10 continua (first_seen_at preservado), .22 sumiu,
	// .33 é novo (first_seen_at = agora).
	t1 := t0.Add(time.Hour)
	hosts2 := []DiscoveryHost{
		{IP: "10.0.0.10", Ports: "445", OSGuess: "windows"},
		{IP: "10.0.0.33", Ports: "22", OSGuess: "ssh", Hostname: "srv-exemplo"},
	}
	if _, err := st.ReplaceDiscoveryHosts(ctx, hosts2, DiscoveryRun{StartedAt: t1, Scanned: 256, Alive: 2, WithoutAgent: 2, StartedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	hs, _ = st.DiscoveryHosts(ctx)
	if len(hs) != 2 {
		t.Fatalf("hosts 2: %+v", hs)
	}
	var h10, h33 DiscoveryHost
	for _, h := range hs {
		switch h.IP {
		case "10.0.0.10":
			h10 = h
		case "10.0.0.33":
			h33 = h
		}
	}
	if !h10.FirstSeenAt.Equal(t0) || !h10.LastSeenAt.Equal(t1) || h10.MatchID != "" { // continua, e desligou do inventário nesta foto
		t.Errorf(".10: %+v", h10)
	}
	if !h33.FirstSeenAt.Equal(t1) || h33.Hostname != "srv-exemplo" {
		t.Errorf(".33: %+v", h33)
	}

	runs, _ := st.DiscoveryRuns(ctx, 10)
	if len(runs) != 2 || runs[0].StartedBy != "ana" || !runs[0].StartedAt.Equal(t1) || runs[1].WithoutAgent != 1 {
		t.Fatalf("varreduras: %+v", runs)
	}
	if runs[1].FinishedAt == nil || !runs[1].FinishedAt.Equal(fin) {
		t.Errorf("finished_at: %+v", runs[1])
	}

	// Varredura vazia (nada vivo) é válida e zera os hosts.
	if _, err := st.ReplaceDiscoveryHosts(ctx, nil, DiscoveryRun{StartedAt: t1.Add(time.Hour), StartedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	if hs, _ := st.DiscoveryHosts(ctx); len(hs) != 0 {
		t.Fatalf("depois de varredura vazia: %+v", hs)
	}
}

func TestDescobertaMemoria(t *testing.T) { testarDescoberta(t, NewMemory()) }

func TestDescobertaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarDescoberta(t, NewPostgres(pool))
}
