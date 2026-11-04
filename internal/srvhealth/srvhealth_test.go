package srvhealth

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

type fakeSys struct {
	s  sample
	in Info
}

func (f *fakeSys) read() sample { return f.s }
func (f *fakeSys) info() Info   { return f.in }

type fakeKeeper struct {
	pool  PoolStat
	saved []Hour
	db    DBStats
}

func (k *fakeKeeper) PoolStat() PoolStat                       { return k.pool }
func (k *fakeKeeper) DBStats(context.Context) (DBStats, error) { return k.db, nil }
func (k *fakeKeeper) SaveHealthHour(_ context.Context, h Hour) error {
	k.saved = append(k.saved, h)
	return nil
}
func (k *fakeKeeper) HealthHours(context.Context, time.Time) ([]Hour, error) { return k.saved, nil }
func (k *fakeKeeper) PruneHealth(context.Context, time.Time) (int64, error)  { return 0, nil }

func TestP95(t *testing.T) {
	var m minuteReq
	for i := 0; i < 95; i++ {
		m.add(20, false, 200, false) // balde 10–25
	}
	for i := 0; i < 5; i++ {
		m.add(1800, true, 500, false) // balde 1000–2500
	}
	m.add(60000, true, 200, true) // um download de um minuto não entra no tempo
	if p := m.p95(); p < 10 || p > 25 {
		t.Fatalf("p95 = %v, quero no balde de 10 a 25 ms", p)
	}
	if m.slow != 5 || m.errors != 5 || m.agent != 6 || m.n != 101 || m.timed != 100 {
		t.Fatalf("lentas/erros/agente/n/timed = %d/%d/%d/%d/%d", m.slow, m.errors, m.agent, m.n, m.timed)
	}
	m.add(1800, false, 200, false)
	if p := m.p95(); p < 1000 {
		t.Fatalf("com 6%% lentas, p95 = %v, quero acima de 1 s", p)
	}
	var empty minuteReq
	if empty.p95() != 0 {
		t.Fatal("sem requisições, p95 0")
	}
}

func TestTickAndHour(t *testing.T) {
	k := &fakeKeeper{pool: PoolStat{Max: 10}}
	sys := &fakeSys{in: Info{Cores: 4, Capacity: 4}}
	m := New(k, []Path{{Name: "dados", Dir: t.TempDir()}}, nil)
	m.sys = sys
	base := time.Date(2026, 11, 3, 9, 58, 0, 0, time.UTC)
	sys.s = sample{at: base, capacity: 4, procOK: true, hostOK: true, hostBusy: 100, hostTotal: 1000,
		memOK: true, memUsed: 1 << 30, memLimit: 4 << 30, hostMemOK: true, hostTotalMem: 8 << 30, hostAvail: 2 << 30}
	m.prime()

	// Minuto 1 (fecha às 9:59): 1 núcleo de 4 por 60 s = 25%; a máquina 50%.
	m.Observe("GET /painel", 200, 30*time.Millisecond, false)
	m.Observe("POST /api/v1/agent/checkin", 503, 2*time.Second, false)
	k.pool = PoolStat{Max: 10, InUse: 3, Acquires: 50, Waits: 5, WaitTime: 500 * time.Millisecond}
	m.watchPool()
	k.pool.InUse = 1
	sys.s.at = base.Add(time.Minute)
	sys.s.procCPU = time.Minute
	sys.s.hostBusy, sys.s.hostTotal = 600, 2000
	if _, done := m.tick(base.Add(time.Minute)); done != nil {
		t.Fatal("a hora não fechou ainda")
	}
	p := m.Points(time.Time{})[0]
	if math.Abs(p.CPUPct-25) > 0.01 || math.Abs(p.HostCPUPct-50) > 0.01 {
		t.Fatalf("CPU = %v / máquina %v", p.CPUPct, p.HostCPUPct)
	}
	if p.MemPct != 25 || p.HostMemPct != 75 {
		t.Fatalf("memória = %v / máquina %v", p.MemPct, p.HostMemPct)
	}
	if p.Requests != 2 || p.AgentRequests != 1 || p.Slow != 1 || p.Errors != 1 {
		t.Fatalf("requisições %+v", p)
	}
	if p.PoolInUse != 3 || p.PoolMax != 10 || p.PoolWaits != 5 || p.PoolAcquires != 50 || p.PoolWaitMs != 500 {
		t.Fatalf("pool: pico %d de %d, esperas %d de %d, %v ms", p.PoolInUse, p.PoolMax, p.PoolWaits, p.PoolAcquires, p.PoolWaitMs)
	}
	if p.DiskFreePct == Unknown && strings.Contains("linux darwin", sys.in.OS) {
		t.Fatal("disco da pasta temporária não medido")
	}
	// Minuto 2 fecha às 10:00 (ainda a hora das 9); o 3 (10:01) fecha a hora.
	sys.s.at = base.Add(2 * time.Minute)
	m.tick(base.Add(2 * time.Minute))
	sys.s.at = base.Add(3 * time.Minute)
	_, done := m.tick(base.Add(3 * time.Minute))
	if done == nil {
		t.Fatal("a hora das 9 devia fechar no minuto 10:01")
	}
	if !done.Hour.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) || done.Samples != 2 || done.Requests != 2 || done.PoolInUseMax != 3 {
		t.Fatalf("hora %+v", *done)
	}
	if done.CPUMax < 25 {
		t.Fatalf("pico da CPU na hora = %v", done.CPUMax)
	}
	m.saveHour(context.Background(), *done, time.Time{})
	if len(k.saved) != 1 {
		t.Fatal("a hora não foi gravada")
	}
	r := m.Routes()
	if len(r) != 2 || r[0].Route != "POST /api/v1/agent/checkin" || r[0].Slow != 1 || r[0].Errors != 1 {
		t.Fatalf("rotas %+v", r)
	}
	if got := m.Points(base.Add(2 * time.Minute)); len(got) != 2 {
		t.Fatalf("Points desde 10:00 = %d", len(got))
	}
}

func TestRingBounded(t *testing.T) {
	m := New(nil, nil, nil)
	m.sys = &fakeSys{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < ringSize+10; i++ {
		m.tick(at.Add(time.Duration(i) * time.Minute))
	}
	if n := len(m.Points(time.Time{})); n != ringSize {
		t.Fatalf("anel com %d pontos", n)
	}
}

func TestObserveNilAndRouteCap(t *testing.T) {
	var none *Monitor
	none.Observe("x", 200, time.Second, false) // não entra em pânico
	m := New(nil, nil, nil)
	for i := 0; i < 600; i++ {
		m.Observe("GET /r"+strings.Repeat("x", i%600)+string(rune('a'+i%26)), 200, time.Millisecond, false)
	}
	if n := len(m.Routes()); n > 501 {
		t.Fatalf("%d rotas guardadas", n)
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[uint64]string{0: "0 B", 1023: "1023 B", 1536: "1,5 KB", 5 << 30: "5,0 GB", 300 << 20: "300 MB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, quero %q", n, got, want)
		}
	}
}
