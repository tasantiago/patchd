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
	perfPass(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), st, agora)
	hs, _ := st.PerfHourly(ctx, "m-1", agora.Add(-48*time.Hour))
	if len(hs) != 1 || !hs[0].Hour.Equal(agora.Add(-time.Hour).Truncate(time.Hour)) || hs[0].CPUAvg != 40 {
		t.Fatalf("horas (só a fechada): %+v", hs)
	}
	ss, _ := st.PerfSamples(ctx, "m-1", agora.Add(-30*24*time.Hour))
	if len(ss) != 2 {
		t.Fatalf("amostras depois da retenção: %+v", ss)
	}
}
