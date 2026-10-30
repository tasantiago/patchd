package perf

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestContas(t *testing.T) {
	c, err := parseProcStat("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3 4\n")
	if err != nil || c.busy != 150 || c.total != 1000 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := parseProcStat("intr 1 2"); err == nil {
		t.Error("sem a linha cpu")
	}
	if v := cpuPct(cpuTimes{busy: 100, total: 1000}, cpuTimes{busy: 400, total: 2000}); v != 30 {
		t.Errorf("cpu %v", v)
	}
	if v := cpuPct(cpuTimes{busy: 100, total: 1000}, cpuTimes{busy: 50, total: 500}); v != 0 {
		t.Errorf("contador reiniciado: %v", v)
	}
	m, err := parseMeminfo("MemTotal:       16000000 kB\nMemFree:  1000 kB\nMemAvailable:    4000000 kB\n")
	if err != nil || m != 75 {
		t.Errorf("meminfo %v %v", m, err)
	}
	if _, err := parseMeminfo("MemTotal: 10 kB\n"); err == nil {
		t.Error("sem MemAvailable")
	}
	vm := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:      100000.\nPages active:    200000.\nPages inactive:  50000.\nPages speculative: 50000.\n"
	if v, err := parseVMStat(vm, 200000*16384*2); err != nil || v != 50 {
		t.Errorf("vm_stat %v %v", v, err)
	}
	if v, err := parsePSCPU(" 50.0\n 30,0\n 20.0\n", 4); err != nil || v != 25 {
		t.Errorf("ps %v %v", v, err)
	}
	d := DiskFree([]protocol.Disk{{Mount: "C:", TotalBytes: 1000, FreeBytes: 123}, {Mount: "D:"}})
	if len(d) != 1 || d["C:"] != 12.3 {
		t.Errorf("discos %v", d)
	}
}

func TestSample(t *testing.T) {
	leituras := []cpuTimes{{busy: 0, total: 100}, {busy: 50, total: 200}, {busy: 150, total: 300}}
	i := 0
	dormiu := time.Duration(0)
	s := &Sampler{
		readCPU: func(context.Context) (cpuTimes, error) { c := leituras[i]; i++; return c, nil },
		readMem: func(context.Context) (float64, error) { return 42, nil },
		sleep:   func(_ context.Context, d time.Duration) error { dormiu += d; return nil },
	}
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p, err := s.Sample(context.Background(), at, nil)
	if err != nil || p.CPU != 50 || p.Mem != 42 || dormiu != time.Second || !p.At.Equal(at) {
		t.Fatalf("primeira: %+v %v %v", p, err, dormiu)
	}
	p, _ = s.Sample(context.Background(), at.Add(time.Minute), nil)
	if p.CPU != 100 || dormiu != time.Second {
		t.Fatalf("segunda (sem nova espera): %+v", p)
	}
	if err := protocol.CheckPerf([]protocol.PerfSample{p}, at); err != nil {
		t.Fatal(err)
	}
}
