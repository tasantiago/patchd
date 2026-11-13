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
	AckPerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error)
	ClosePerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error)
	RollupPerf(ctx context.Context, from, to time.Time) (int64, error)
	RollupLatePerf(ctx context.Context) (int64, error)
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
	// Amostra atrasada (a hora dela já passou da janela da passada regular): só a hora dela
	// é marcada e refeita (as amostras da hora corrente, acima, não marcaram nada), e a marca
	// some depois de usada.
	velha := t0.Add(-20 * time.Hour)
	if _, err := st.SavePerf(ctx, id, []protocol.PerfSample{{At: velha.Add(5 * time.Minute), CPU: 10, Mem: 20}, {At: velha.Add(6 * time.Minute), CPU: 30, Mem: 40}},
		p, t0.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RollupLatePerf(ctx); err != nil || n != 1 {
		t.Fatalf("horas atrasadas montadas: %d %v", n, err)
	}
	hs, _ = st.PerfHourly(ctx, id, t0.Add(-24*time.Hour))
	if len(hs) != 2 || !hs[0].Hour.Equal(velha) || hs[0].Samples != 2 || hs[0].CPUAvg != 20 || hs[0].CPUMax != 30 || hs[1].Samples != 7 {
		t.Fatalf("a hora atrasada entra, a recente fica para a passada regular: %+v", hs)
	}
	if n, err := st.RollupLatePerf(ctx); err != nil || n != 0 {
		t.Fatalf("a marca some depois de usada: %d %v", n, err)
	}
	if _, err := st.PrunePerf(ctx, t0.Add(time.Hour), t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ss, _ := st.PerfSamples(ctx, id, t0.Add(-48*time.Hour)); len(ss) != 0 {
		t.Errorf("amostras depois da retenção: %d", len(ss))
	}
	if hs, _ := st.PerfHourly(ctx, id, t0.Add(-time.Hour)); len(hs) != 1 {
		t.Errorf("horas depois da retenção: %d", len(hs))
	}
	if l, _ := st.PerfAlerts(ctx, PerfAlertFilter{}); len(l) != 0 {
		t.Errorf("alertas fechados depois da retenção: %+v", l)
	}

	// Em lote: reconhecer só os abertos sem reconhecimento; fechar à mão zera a contagem, e a
	// CPU que continua alta abre um alerta novo depois dos minutos seguidos.
	if ch, _ := st.SavePerf(ctx, id, lote(10, 95, 95, 95), p, t0); len(ch) != 1 || !ch[0].Opened {
		t.Fatalf("abrir de novo: %+v", ch)
	}
	l, _ = st.PerfAlerts(ctx, PerfAlertFilter{OpenOnly: true})
	if n, err := st.AckPerfAlerts(ctx, []int64{l[0].ID, 999999}, "ana", "lote", t0); err != nil || n != 1 {
		t.Fatalf("reconhecer em lote: %d %v", n, err)
	}
	if n, _ := st.AckPerfAlerts(ctx, []int64{l[0].ID}, "ana", "lote", t0); n != 0 {
		t.Error("reconheceu duas vezes")
	}
	at := t0.Add(13 * time.Minute)
	if n, err := st.ClosePerfAlerts(ctx, []int64{l[0].ID}, "thyago", "máquina em manutenção", at); err != nil || n != 1 {
		t.Fatalf("fechar em lote: %d %v", n, err)
	}
	if n, _ := st.ClosePerfAlerts(ctx, []int64{l[0].ID}, "thyago", "", at); n != 0 {
		t.Error("fechou duas vezes")
	}
	f, _ := st.PerfAlerts(ctx, PerfAlertFilter{MachineID: id})
	if len(f) != 1 || f[0].ClosedAt == nil || f[0].CloseBy != "thyago" || f[0].CloseNote != "máquina em manutenção" || f[0].AckBy != "ana" {
		t.Fatalf("fechado à mão: %+v", f)
	}
	if ch, _ := st.SavePerf(ctx, id, lote(13, 95, 95), p, t0); len(ch) != 0 {
		t.Fatalf("antes dos minutos seguidos: %+v", ch)
	}
	if ch, _ := st.SavePerf(ctx, id, lote(15, 95), p, t0); len(ch) != 1 || !ch[0].Opened {
		t.Fatalf("o problema continua, alerta novo: %+v", ch)
	}
	if o, _, _ := st.PerfAlertCounts(ctx); o != 1 {
		t.Errorf("abertos: %d", o)
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
