package srvhealth

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// day: 24 horas de minutos iguais, com fill aplicado a cada um.
func day(fill func(i int, p *Point)) []Point {
	out := make([]Point, ringSize)
	for i := range out {
		p := Point{At: now.Add(time.Duration(i-ringSize) * time.Minute), CPUPct: 10, HostCPUPct: 20, MemPct: 30, MemBytes: 300 << 20,
			HostMemPct: 40, Requests: 20, Timed: 20, LatAvgMs: 30, LatP95Ms: 80, PoolMax: 10, PoolInUse: 2, PoolAcquires: 100, DiskFreePct: 60}
		if fill != nil {
			fill(i, &p)
		}
		out[i] = p
	}
	return out
}

func baseInput(pts []Point) Input {
	return Input{Now: now, Info: Info{Cores: 4, Capacity: 4, MemLimit: 1 << 30, HostMemTotal: 8 << 30}, Points: pts,
		Pool: PoolStat{Max: 10}, DB: &DBStats{Bytes: 2 << 30, Connections: 12, MaxConnections: 100, CacheHitPct: 99.5, BlocksRead: 1e6},
		Disks: []Disk{{Name: "conteúdo", Dir: "/var/lib/patchd/conteudo", Total: 100 << 30, Free: 60 << 30, FreePct: 60, statOK: true}}}
}

func resources(r Report) string {
	var out []string
	for _, a := range r.Advice {
		out = append(out, a.Resource+":"+a.Level)
	}
	return strings.Join(out, ",")
}

func TestAdviseHealthy(t *testing.T) {
	r := Advise(baseInput(day(nil)))
	if r.Level != LevelOK || len(r.Advice) != 0 || r.Verdict != "Nada a aumentar agora" {
		t.Fatalf("%s: %s", r.Verdict, resources(r))
	}
	if !strings.Contains(r.Capacity, "7,0×") {
		t.Fatalf("folga da CPU: %q", r.Capacity)
	}
	if len(r.Gauges) != 5 {
		t.Fatalf("%d medidores", len(r.Gauges))
	}
}

func TestAdviseSpikeIgnored(t *testing.T) {
	// Um pico de 10 minutos a 100% não pede CPU: o p95 olha 72 minutos.
	r := Advise(baseInput(day(func(i int, p *Point) {
		if i < 10 {
			p.CPUPct = 100
		}
	})))
	if len(r.Advice) != 0 {
		t.Fatalf("pico isolado virou recomendação: %s", resources(r))
	}
}

func TestAdviseCPUAndLatency(t *testing.T) {
	r := Advise(baseInput(day(func(i int, p *Point) {
		if i%10 == 0 {
			p.CPUPct, p.LatP95Ms = 95, 4000
		}
	})))
	if r.Level != LevelCritical || !strings.HasPrefix(r.Verdict, "Aumente agora: cpu") {
		t.Fatalf("%s: %s", r.Verdict, resources(r))
	}
	var lat *Advice
	for i := range r.Advice {
		if r.Advice[i].Resource == ResLatency {
			lat = &r.Advice[i]
		}
	}
	if lat == nil || !strings.Contains(lat.Action, "CPU") {
		t.Fatalf("a lentidão devia apontar a CPU: %+v", r.Advice)
	}
}

func TestAdviseQuota(t *testing.T) {
	in := baseInput(day(func(_ int, p *Point) { p.CPUPct = 80 }))
	in.Info.CPUQuota, in.Info.Capacity = 1, 1
	r := Advise(in)
	if len(r.Advice) == 0 || !strings.Contains(r.Advice[0].Action, "cota de CPU do container") {
		t.Fatalf("%+v", r.Advice)
	}
}

func TestAdviseHostBusy(t *testing.T) {
	r := Advise(baseInput(day(func(_ int, p *Point) { p.HostCPUPct = 95 })))
	if resources(r) != "CPU:critico" || !strings.Contains(r.Advice[0].Why, "PostgreSQL") {
		t.Fatalf("%s %+v", resources(r), r.Advice)
	}
}

func TestAdviseMemory(t *testing.T) {
	in := baseInput(day(func(_ int, p *Point) { p.MemPct, p.MemBytes = 92, 940<<20 }))
	in.Info.MemFromCG = true
	r := Advise(in)
	if resources(r) != "Memória:critico" || !strings.Contains(r.Advice[0].Action, "mem_limit") || !strings.Contains(r.Advice[0].Action, "1,4 GB") {
		t.Fatalf("%s %+v", resources(r), r.Advice)
	}
}

func TestAdvisePool(t *testing.T) {
	r := Advise(baseInput(day(func(i int, p *Point) {
		p.PoolWaits, p.PoolWaitMs = 10, 300
		if i%20 == 0 {
			p.PoolInUse = 10
		}
	})))
	if resources(r) != "Banco:atencao" || !strings.Contains(r.Advice[0].Action, "pool_max_conns") || !strings.Contains(r.Advice[0].Action, "tente 20") {
		t.Fatalf("%s %+v", resources(r), r.Advice)
	}
}

func TestAdviseDB(t *testing.T) {
	in := baseInput(day(nil))
	in.DB.Connections, in.DB.CacheHitPct = 95, 85
	r := Advise(in)
	if resources(r) != "Banco:critico,Banco:critico" {
		t.Fatalf("%s", resources(r))
	}
	in.DB.CacheHitPct, in.DB.BlocksRead, in.DB.Connections = 50, 100, 10 // pouca leitura: não conta
	if r := Advise(in); len(r.Advice) != 0 {
		t.Fatalf("%s", resources(r))
	}
}

func TestAdviseDisk(t *testing.T) {
	in := baseInput(day(nil))
	in.Disks[0].Free, in.Disks[0].FreePct = 8<<30, 8
	in.Disks = append(in.Disks, Disk{Name: "cache", SameAs: "conteúdo"}) // não medido de novo
	// O banco cresceu 1 GiB em 4 dias: 256 MiB por dia; 8 GiB acabam em 32 dias.
	in.Hours = []Hour{{Hour: now.Add(-96 * time.Hour), DBBytes: 1 << 30}}
	r := Advise(in)
	if resources(r) != "Disco:critico,Disco:atencao" {
		t.Fatalf("%s", resources(r))
	}
	if !strings.Contains(r.Growth, "256 MB") || !strings.Contains(r.Advice[1].Title, "32 dias") {
		t.Fatalf("%q %+v", r.Growth, r.Advice)
	}
}

func TestAdviseWarming(t *testing.T) {
	pts := day(func(_ int, p *Point) { p.CPUPct = 100 })[:5]
	r := Advise(baseInput(pts))
	if !r.Warming || r.Verdict != "Medindo" || len(r.Advice) != 0 {
		t.Fatalf("%s %s", r.Verdict, resources(r))
	}
}

func TestAdviseErrors(t *testing.T) {
	r := Advise(baseInput(day(func(i int, p *Point) {
		if i%10 == 0 {
			p.Errors = 5
		}
	})))
	if resources(r) != "Tempo de resposta:atencao" {
		t.Fatalf("%s", resources(r))
	}
}

func TestGrowth(t *testing.T) {
	if g := dbGrowthPerDay(nil, &DBStats{Bytes: 1}, now); g != 0 {
		t.Fatal("sem horas, sem crescimento")
	}
	hours := []Hour{{Hour: now.Add(-10 * time.Hour), DBBytes: 100}}
	if g := dbGrowthPerDay(hours, &DBStats{Bytes: 200}, now); g != 0 {
		t.Fatal("menos de um dia não conta")
	}
	hours = []Hour{{Hour: now.Add(-48 * time.Hour), DBBytes: 300}}
	if g := dbGrowthPerDay(hours, &DBStats{Bytes: 200}, now); g != 0 {
		t.Fatal("encolheu: zero")
	}
}

// O banco fica no armazenamento dos volumes (Path.DB): a previsão usa esse disco, não o mais
// cheio. As versões do agente num /home de 10 GB não fazem o banco "encher o disco".
func TestAdviseDiskDoBanco(t *testing.T) {
	in := baseInput(day(nil))
	in.Disks = []Disk{
		{Name: "Conteúdo", DB: true, Total: 160 << 30, Free: 139 << 30, FreePct: 87, statOK: true},
		{Name: "Cache de repositórios", DB: true, SameAs: "Conteúdo"},
		{Name: "Versões do agente", Total: 10 << 30, Free: 6900 << 20, FreePct: 67, statOK: true},
	}
	// 517 MiB por dia: o /home (6,9 GB) acabaria em 14 dias; o volume do banco, em 275.
	in.Hours = []Hour{{Hour: now.Add(-96 * time.Hour), DBBytes: 1 << 30}}
	in.DB = &DBStats{Bytes: 1<<30 + 4*517<<20}
	if r := Advise(in); resources(r) != "" {
		t.Fatalf("aviso falso: %s %+v", resources(r), r.Advice)
	}
	// Sem saber onde o banco fica (fora de container): por cautela, o disco mais cheio.
	in.Disks[0].DB, in.Disks[1].DB = false, false
	r := Advise(in)
	if resources(r) != "Disco:critico" || !strings.Contains(r.Advice[0].Title, "13 dias") {
		t.Fatalf("%s %+v", resources(r), r.Advice)
	}
}
