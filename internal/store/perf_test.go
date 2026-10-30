package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoDesempenho interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SavePerf(ctx context.Context, machineID string, samples []protocol.PerfSample, policy protocol.MonitorPolicy, now time.Time) ([]perfeval.Change, error)
	PerfSamples(ctx context.Context, machineID string, since time.Time) ([]protocol.PerfSample, error)
	PerfHourly(ctx context.Context, machineID string, since time.Time) ([]PerfHour, error)
	PerfAlerts(ctx context.Context, f PerfAlertFilter) ([]PerfAlert, error)
	AckPerfAlert(ctx context.Context, id int64, by, note string, at time.Time) (bool, error)
	PerfAlertCounts(ctx context.Context) (int, int, error)
	RollupPerf(ctx context.Context, from, to time.Time) (int64, error)
	PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error)
}

// docs/monitoramento.md: as amostras viram alertas pela regra, um lote repetido não conta duas
// vezes, as horas saem das amostras e a retenção apaga o que passou.
func testarDesempenho(t *testing.T, st contratoDesempenho) {
	ctx := context.Background()
	const id = "perf-0001"
	if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h", OS: protocol.OSInfo{Hostname: "pc"}}); err != nil {
		t.Fatal(err)
	}
	p := protocol.MonitorPolicy{CPUPct: 90, MemPct: 90, DiskFreePct: 10, Minutes: 3}
	t0 := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	lote := func(from int, cpus ...float64) []protocol.PerfSample {
		var out []protocol.PerfSample
		for i, c := range cpus {
			out = append(out, protocol.PerfSample{At: t0.Add(time.Duration(from+i) * time.Minute), CPU: c, Mem: 40,
				Disks: map[string]float64{"C:": 50, "D:": 30}})
		}
		return out
	}
	ch, err := st.SavePerf(ctx, id, lote(0, 95, 96, 97, 99), p, t0)
	if err != nil || len(ch) != 1 || !ch[0].Opened {
		t.Fatalf("abrir: %+v %v", ch, err)
	}
	if ch, err := st.SavePerf(ctx, id, lote(0, 95, 96, 97, 99), p, t0); err != nil || len(ch) != 0 {
		t.Fatalf("lote repetido: %+v %v", ch, err)
	}
	l, _ := st.PerfAlerts(ctx, PerfAlertFilter{OpenOnly: true})
	if len(l) != 1 || l[0].Metric != "cpu" || l[0].OpenValue != 97 || l[0].Peak != 99 || l[0].Last != 99 || l[0].ClosedAt != nil {
		t.Fatalf("aberto: %+v", l)
	}
	if o, u, _ := st.PerfAlertCounts(ctx); o != 1 || u != 1 {
		t.Errorf("contagem: %d %d", o, u)
	}
	if ok, err := st.AckPerfAlert(ctx, l[0].ID, "thyago", "chamado 1", t0); err != nil || !ok {
		t.Fatalf("reconhecer: %v %v", ok, err)
	}
	if ok, _ := st.AckPerfAlert(ctx, l[0].ID, "ana", "de novo", t0); ok {
		t.Error("reconheceu duas vezes")
	}
	if o, u, _ := st.PerfAlertCounts(ctx); o != 1 || u != 0 {
		t.Errorf("contagem depois do reconhecimento: %d %d", o, u)
	}
	if ch, _ := st.SavePerf(ctx, id, lote(4, 20, 20, 20), p, t0); len(ch) != 1 || ch[0].Opened {
		t.Fatalf("fechar: %+v", ch)
	}
	l, _ = st.PerfAlerts(ctx, PerfAlertFilter{MachineID: id})
	if len(l) != 1 || l[0].ClosedAt == nil || l[0].Peak != 99 || l[0].AckBy != "thyago" {
		t.Fatalf("fechado: %+v", l)
	}
	ss, _ := st.PerfSamples(ctx, id, t0.Add(2*time.Minute))
	if len(ss) != 5 || ss[0].CPU != 97 || ss[0].Disks["D:"] != 30 {
		t.Fatalf("amostras: %+v", ss)
	}
	if _, err := st.RollupPerf(ctx, t0.Add(-time.Hour), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	hs, _ := st.PerfHourly(ctx, id, t0.Add(-time.Hour))
	if len(hs) != 1 || !hs[0].Hour.Equal(t0) || hs[0].Samples != 7 || hs[0].CPUMax != 99 || hs[0].DiskMin == nil || *hs[0].DiskMin != 30 {
		t.Fatalf("horas: %+v", hs)
	}
	if _, err := st.PrunePerf(ctx, t0.Add(time.Hour), t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ss, _ := st.PerfSamples(ctx, id, t0.Add(-time.Hour)); len(ss) != 0 {
		t.Errorf("amostras depois da retenção: %d", len(ss))
	}
	if hs, _ := st.PerfHourly(ctx, id, t0.Add(-time.Hour)); len(hs) != 1 {
		t.Errorf("horas depois da retenção: %d", len(hs))
	}
	if l, _ := st.PerfAlerts(ctx, PerfAlertFilter{}); len(l) != 0 {
		t.Errorf("alertas fechados depois da retenção: %+v", l)
	}
}

func TestDesempenhoMemoria(t *testing.T) { testarDesempenho(t, NewMemory()) }

func TestDesempenhoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarDesempenho(t, NewPostgres(pool))
}
