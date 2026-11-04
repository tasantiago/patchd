package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/srvhealth"
)

func TestSaudeServidorPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	pg := NewPostgres(pool)
	if st := pg.PoolStat(); st.Max <= 0 {
		t.Fatalf("pool %+v", st)
	}
	db, err := pg.DBStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db.Bytes <= 0 || db.MaxConnections <= 0 || db.Connections <= 0 || len(db.Tables) == 0 {
		t.Fatalf("banco %+v", db)
	}
	hour := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	h := srvhealth.Hour{Hour: hour, Samples: 30, CPUAvg: 10, CPUMax: 40, HostCPUAvg: srvhealth.Unknown, HostCPUMax: srvhealth.Unknown,
		MemAvg: 20, MemMax: 25, MemBytesMax: 100 << 20, HostMemMax: 50, Requests: 100, LatAvgMs: 10, LatP95Max: 90,
		PoolInUseMax: 3, PoolMax: 10, PoolWaits: 1, DiskFreeMin: srvhealth.Unknown, DBBytes: 1000, Goroutines: 40}
	if err := pg.SaveHealthHour(ctx, h); err != nil {
		t.Fatal(err)
	}
	// O processo reiniciou no meio da hora: a segunda metade junta com a primeira.
	h2 := h
	h2.CPUAvg, h2.CPUMax, h2.Requests, h2.LatAvgMs, h2.HostCPUAvg, h2.HostCPUMax, h2.DiskFreeMin = 30, 80, 300, 30, 50, 60, 40
	if err := pg.SaveHealthHour(ctx, h2); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveHealthHour(ctx, srvhealth.Hour{Hour: hour.Add(-100 * 24 * time.Hour), Samples: 1, HostCPUAvg: -1, HostCPUMax: -1, HostMemMax: -1, DiskFreeMin: -1}); err != nil {
		t.Fatal(err)
	}
	got, err := pg.HealthHours(ctx, hour.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d horas", len(got))
	}
	g := got[0]
	if g.Samples != 60 || g.CPUAvg != 20 || g.CPUMax != 80 || g.Requests != 400 || g.LatAvgMs != 25 || g.HostCPUAvg != 50 || g.DiskFreeMin != 40 {
		t.Fatalf("hora juntada %+v", g)
	}
	n, err := pg.PruneHealth(ctx, hour.Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("retenção apagou %d (%v)", n, err)
	}
}
