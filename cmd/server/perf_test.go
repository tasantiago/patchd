package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A passada de hora em hora monta as horas fechadas e apaga as amostras de mais de 7 dias.
func TestPassadaDoDesempenho(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	if _, err := st.SaveInventory(ctx, "m-1", protocol.InventoryReport{SchemaVersion: 1, Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	agora := time.Date(2026, 10, 30, 12, 30, 0, 0, time.UTC)
	velha := protocol.PerfSample{At: agora.Add(-8 * 24 * time.Hour), CPU: 1, Mem: 1}
	hora := protocol.PerfSample{At: agora.Add(-time.Hour), CPU: 40, Mem: 50, Disks: map[string]float64{"/": 20}}
	atual := protocol.PerfSample{At: agora.Add(-time.Minute), CPU: 60, Mem: 50}
	if _, err := st.SavePerf(ctx, "m-1", []protocol.PerfSample{velha, hora, atual}, protocol.DefaultMonitorPolicy, agora); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if !perfPass(ctx, log, st, agora, true) {
		t.Fatal("a passada não montou as horas")
	}
	hs, _ := st.PerfHourly(ctx, "m-1", agora.Add(-48*time.Hour))
	if len(hs) != 1 || !hs[0].Hour.Equal(agora.Add(-time.Hour).Truncate(time.Hour)) || hs[0].CPUAvg != 40 {
		t.Fatalf("horas (só a fechada): %+v", hs)
	}
	// Depois da primeira, a passada só refaz as horas recentes; a amostra que chega atrasada
	// (o notebook que voltou para a rede) marca a hora dela, e a passada seguinte a monta.
	atrasada := protocol.PerfSample{At: agora.Add(-20 * time.Hour), CPU: 80, Mem: 10}
	recente := protocol.PerfSample{At: agora.Add(-time.Hour).Add(time.Minute), CPU: 20, Mem: 50}
	if _, err := st.SavePerf(ctx, "m-1", []protocol.PerfSample{atrasada, recente}, protocol.DefaultMonitorPolicy, agora); err != nil {
		t.Fatal(err)
	}
	if !perfPass(ctx, log, st, agora, false) {
		t.Fatal("a segunda passada não montou as horas")
	}
	hs, _ = st.PerfHourly(ctx, "m-1", agora.Add(-48*time.Hour))
	if len(hs) != 2 || !hs[0].Hour.Equal(atrasada.At.Truncate(time.Hour)) || hs[0].CPUAvg != 80 || hs[1].CPUAvg != 30 || hs[1].Samples != 2 {
		t.Fatalf("a hora atrasada e a recente refeita: %+v", hs)
	}
	ss, _ := st.PerfSamples(ctx, "m-1", agora.Add(-30*24*time.Hour))
	if len(ss) != 4 || ss[0].At.Before(agora.Add(-perfSamplesKeep)) {
		t.Fatalf("amostras depois da retenção (a de 8 dias sai): %+v", ss)
	}
}
