// Package srvhealth mede a saúde do próprio servidor do patchd (docs/saude-servidor.md): a
// cada minuto, quanto da CPU e da memória disponíveis o processo usa, quanto a máquina inteira
// usa, o pool de conexões do banco, o espaço das pastas de dados e o tempo das requisições. O
// minuto fica num anel de 24 horas na memória; a hora fechada vai para o banco (90 dias) pelo
// Keeper. As regras de Advise dizem o que aumentar.
//
// Só a biblioteca padrão e golang.org/x/sys: nada de agente externo. No Linux, os números vêm
// do /proc e do cgroup (dentro do container, o limite do container é o que vale); nos outros
// sistemas, só o que o runtime do Go sabe.
package srvhealth

import (
	"context"
	"log/slog"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Unknown marca uma medida que este sistema não tem (fora do Linux, ou sem /proc).
const Unknown = -1

// Point é um minuto.
type Point struct {
	At time.Time
	// CPUPct: a CPU do processo, em % da capacidade dele (os núcleos, ou a cota do cgroup).
	CPUPct float64
	// HostCPUPct: a CPU da máquina inteira (o PostgreSQL e os outros processos também). Unknown
	// fora do Linux.
	HostCPUPct float64
	// MemPct: a memória em uso, em % do limite (o do cgroup, ou a memória da máquina).
	MemPct   float64
	MemBytes uint64
	// HostMemPct: a memória da máquina em uso (100 - MemAvailable). Unknown fora do Linux.
	HostMemPct float64
	HeapBytes  uint64
	Goroutines int
	GCPauseMs  float64 // pausas do GC no minuto, somadas
	Load1      float64 // carga média de 1 minuto; Unknown fora do Linux
	// As requisições do minuto (as da API dos agentes também em AgentRequests).
	Requests, AgentRequests, Slow, Errors int
	Timed                                 int // as requisições no tempo (sem os downloads)
	LatAvgMs, LatP95Ms                    float64
	// O pool do banco: o pico de conexões em uso no minuto, o máximo e as esperas por conexão.
	PoolInUse, PoolMax int32
	PoolAcquires       int64
	PoolWaits          int64
	PoolWaitMs         float64 // tempo total esperando conexão no minuto
	// DiskFreePct: o menor espaço livre entre as pastas de dados; Unknown sem pasta.
	DiskFreePct float64
}

// PoolPct: o pico do minuto em % do máximo do pool (Unknown sem banco).
func (p Point) PoolPct() float64 {
	if p.PoolMax <= 0 {
		return Unknown
	}
	return 100 * float64(p.PoolInUse) / float64(p.PoolMax)
}

// PoolStat é o retrato do pool de conexões (pgxpool.Stat, sem depender do pgx).
type PoolStat struct {
	Max, Total, InUse int32
	Acquires, Waits   int64         // acumulados desde a partida
	WaitTime          time.Duration // acumulado: só as esperas por pool vazio
}

// Path é uma pasta de dados cujo disco interessa.
type Path struct{ Name, Dir string }

// Disk é o espaço de uma pasta agora.
type Disk struct {
	Name, Dir   string
	Total, Free uint64
	FreePct     float64
	Err         string
	SameAs      string // outra pasta do mesmo volume (contada uma vez só)
	device      uint64
	deviceKnown bool
	statOK      bool
}

// Measured: o espaço foi lido (e o volume não repete outra pasta).
func (d Disk) Measured() bool { return d.statOK }

// Hour é a hora fechada que vai para o banco.
type Hour struct {
	Hour                   time.Time
	Samples                int
	CPUAvg, CPUMax         float64
	HostCPUAvg, HostCPUMax float64 // Unknown fora do Linux
	MemAvg, MemMax         float64
	MemBytesMax            uint64
	HostMemMax             float64
	Requests, Slow, Errors int64
	LatAvgMs, LatP95Max    float64
	PoolInUseMax, PoolMax  int32
	PoolWaits              int64
	DiskFreeMin            float64
	DBBytes                int64 // o tamanho do banco no fim da hora (0: sem banco)
	Goroutines             int
}

// DBStats é o que o PostgreSQL conta sobre si.
type DBStats struct {
	Bytes          int64
	Connections    int // conexões abertas no servidor do banco (de todos os clientes)
	MaxConnections int
	CacheHitPct    float64 // blocos lidos da memória do PostgreSQL; Unknown sem leitura bastante
	BlocksRead     int64
	Deadlocks      int64
	TempBytes      int64
	LongestQuery   time.Duration // a consulta ativa mais antiga
	Tables         []TableSize   // as maiores, em ordem
}

// TableSize é o tamanho de uma tabela (dados, índices e TOAST).
type TableSize struct {
	Name  string
	Bytes int64
}

// Keeper guarda as horas e lê o banco (o PostgreSQL; sem ele, a tela mostra só as 24 horas).
type Keeper interface {
	PoolStat() PoolStat
	DBStats(ctx context.Context) (DBStats, error)
	SaveHealthHour(ctx context.Context, h Hour) error
	HealthHours(ctx context.Context, from time.Time) ([]Hour, error)
	PruneHealth(ctx context.Context, before time.Time) (int64, error)
}

// Info é o que não muda (ou quase) e a tela mostra.
type Info struct {
	Cores        int     // runtime.NumCPU
	CPUQuota     float64 // núcleos da cota do cgroup; 0: sem cota
	Capacity     float64 // os núcleos que o processo pode usar (o menor dos dois)
	GOMAXPROCS   int
	MemLimit     uint64 // o limite que vale (cgroup, ou a memória da máquina)
	MemFromCG    bool   // o limite é do cgroup (container)
	HostMemTotal uint64 // 0: desconhecida
	GoMemLimit   int64  // debug.SetMemoryLimit; math.MaxInt64 sem limite
	OS           string
}

// --- As requisições --------------------------------------------------------------------

// latBuckets: os limites dos baldes do histograma de tempo, em milissegundos.
var latBuckets = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// SlowMs: uma requisição acima disto conta como lenta.
const SlowMs = 1000

type minuteReq struct {
	n, agent, slow, errors int
	timed                  int     // as que entram no tempo (sem as transferências de arquivo)
	sum                    float64 // ms
	hist                   [13]int // len(latBuckets)+1
}

// add conta uma requisição. transfer: um download de arquivo (catálogo, pacote, versão do
// agente), cujo tempo é o da rede do cliente: conta no volume, não no tempo de resposta.
func (m *minuteReq) add(ms float64, agent bool, status int, transfer bool) {
	m.n++
	if agent {
		m.agent++
	}
	if status >= 500 {
		m.errors++
	}
	if transfer {
		return
	}
	m.timed++
	m.sum += ms
	if ms > SlowMs {
		m.slow++
	}
	i := sort.SearchFloat64s(latBuckets, ms)
	m.hist[i]++
}

// p95 estima o percentil 95 pelo histograma (interpolação dentro do balde).
func (m *minuteReq) p95() float64 {
	if m.timed == 0 {
		return 0
	}
	target := 0.95 * float64(m.timed)
	cum := 0.0
	for i, c := range m.hist {
		if c == 0 {
			continue
		}
		if cum+float64(c) >= target {
			lo := 0.0
			if i > 0 {
				lo = latBuckets[i-1]
			}
			hi := lo * 2
			if i < len(latBuckets) {
				hi = latBuckets[i]
			}
			return lo + (hi-lo)*(target-cum)/float64(c)
		}
		cum += float64(c)
	}
	return latBuckets[len(latBuckets)-1]
}

// RouteStat é uma rota desde a partida.
type RouteStat struct {
	Route               string
	Count, Slow, Errors int64
	AvgMs, MaxMs        float64
	Transfer            bool // download de arquivo: o tempo é o da rede do cliente
	sumMs               float64
}

// --- O monitor -------------------------------------------------------------------------

// ringSize: 24 horas de minutos.
const ringSize = 24 * 60

// poolTick: o pool é olhado com esta frequência, para o pico do minuto não escapar.
const poolTick = 5 * time.Second

// Monitor junta tudo. O zero não serve: use New.
type Monitor struct {
	keeper Keeper // nil: sem banco
	paths  []Path
	logger *slog.Logger
	now    func() time.Time
	start  time.Time
	sys    system

	mu      sync.Mutex
	cur     minuteReq
	routes  map[string]*RouteStat
	ring    []Point // em ordem; no máximo ringSize
	poolMax int32   // o pico de conexões em uso desde o último ponto
	hourAgg []Point // os pontos da hora em andamento (para a Hour)

	prev     sample // a leitura do minuto anterior (os deltas)
	prevPool PoolStat
	prevGC   uint32
	prevGCNs uint64
}

// New cria o monitor. keeper pode ser nil (sem banco); paths são as pastas de dados.
func New(keeper Keeper, paths []Path, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Monitor{keeper: keeper, paths: paths, logger: logger, now: time.Now, routes: map[string]*RouteStat{}, sys: newSystem()}
	m.start = m.now()
	return m
}

// Observe registra uma requisição atendida. route é o padrão do ServeMux (nunca o caminho
// cru: o número de rotas é limitado); transfer: um download de arquivo (fica fora do tempo
// de resposta, que mede o servidor, não a rede de quem baixa).
func (m *Monitor) Observe(route string, status int, d time.Duration, transfer bool) {
	if m == nil {
		return
	}
	ms := float64(d) / float64(time.Millisecond)
	agent := strings.Contains(route, "/api/")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur.add(ms, agent, status, transfer)
	rs := m.routes[route]
	if rs == nil {
		if len(m.routes) >= 500 { // segurança: rotas são padrões, não caminhos
			route = "(outras)"
			if rs = m.routes[route]; rs == nil {
				rs = &RouteStat{Route: route}
				m.routes[route] = rs
			}
		} else {
			rs = &RouteStat{Route: route}
			m.routes[route] = rs
		}
	}
	rs.Count++
	rs.sumMs += ms
	rs.MaxMs = math.Max(rs.MaxMs, ms)
	rs.Transfer = rs.Transfer || transfer
	if ms > SlowMs && !transfer {
		rs.Slow++
	}
	if status >= 500 {
		rs.Errors++
	}
}

// Run mede a cada minuto até o ctx acabar; na virada da hora, grava a hora fechada.
func (m *Monitor) Run(ctx context.Context) {
	m.mu.Lock()
	m.prime()
	m.mu.Unlock()
	pool := time.NewTicker(poolTick)
	defer pool.Stop()
	next := m.now().Truncate(time.Minute).Add(time.Minute)
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pool.C:
			m.watchPool()
		case <-timer.C:
			hour, done := m.tick(next)
			if done != nil {
				m.saveHour(ctx, *done, hour)
			}
			next = next.Add(time.Minute)
			if d := time.Until(next); d > 0 {
				timer.Reset(d)
			} else {
				next = m.now().Truncate(time.Minute).Add(time.Minute)
				timer.Reset(time.Until(next))
			}
		}
	}
}

func (m *Monitor) prime() {
	m.prev = m.sys.read()
	if m.keeper != nil {
		m.prevPool = m.keeper.PoolStat()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.prevGC, m.prevGCNs = ms.NumGC, ms.PauseTotalNs
}

func (m *Monitor) watchPool() {
	if m.keeper == nil {
		return
	}
	st := m.keeper.PoolStat()
	m.mu.Lock()
	m.poolMax = max(m.poolMax, st.InUse)
	m.mu.Unlock()
}

// tick fecha o minuto at. Devolve a hora fechada, se o minuto virou a hora.
func (m *Monitor) tick(at time.Time) (time.Time, *Hour) {
	s := m.sys.read()
	var pool PoolStat
	if m.keeper != nil {
		pool = m.keeper.PoolStat()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	disks := m.Disks()

	m.mu.Lock()
	defer m.mu.Unlock()
	p := Point{At: at, Goroutines: runtime.NumGoroutine(), HeapBytes: ms.HeapAlloc}
	p.CPUPct, p.HostCPUPct = cpuPct(m.prev, s), hostCPUPct(m.prev, s)
	p.MemBytes, p.MemPct, p.HostMemPct = memOf(s)
	p.Load1 = s.load1
	p.GCPauseMs = float64(ms.PauseTotalNs-m.prevGCNs) / 1e6
	m.prevGC, m.prevGCNs = ms.NumGC, ms.PauseTotalNs
	p.Requests, p.AgentRequests, p.Slow, p.Errors = m.cur.n, m.cur.agent, m.cur.slow, m.cur.errors
	if m.cur.timed > 0 {
		p.LatAvgMs = m.cur.sum / float64(m.cur.timed)
		p.LatP95Ms = m.cur.p95()
	}
	p.Timed = m.cur.timed
	m.cur = minuteReq{}
	if m.keeper != nil {
		p.PoolMax = pool.Max
		p.PoolInUse = max(m.poolMax, pool.InUse)
		p.PoolAcquires = max(0, pool.Acquires-m.prevPool.Acquires)
		p.PoolWaits = max(0, pool.Waits-m.prevPool.Waits)
		p.PoolWaitMs = math.Max(0, float64(pool.WaitTime-m.prevPool.WaitTime)/float64(time.Millisecond))
		m.prevPool = pool
		m.poolMax = 0
	}
	p.DiskFreePct = Unknown
	for _, d := range disks {
		if d.statOK && (p.DiskFreePct == Unknown || d.FreePct < p.DiskFreePct) {
			p.DiskFreePct = d.FreePct
		}
	}
	m.prev = s
	m.ring = append(m.ring, p)
	if len(m.ring) > ringSize {
		m.ring = append(m.ring[:0:0], m.ring[len(m.ring)-ringSize:]...)
	}
	// A hora: os pontos de [h, h+1h) fecham quando chega o primeiro de h+1h.
	var done *Hour
	if len(m.hourAgg) > 0 && !sameHour(m.hourAgg[0].At, p.At) {
		h := aggregate(m.hourAgg)
		done = &h
		m.hourAgg = nil
	}
	m.hourAgg = append(m.hourAgg, p)
	return at, done
}

// hourOf: a hora de um ponto. O ponto "At" é o fim do minuto medido; o minuto 10:00 fecha
// 09:59–10:00 e pertence à hora das 9.
func hourOf(t time.Time) time.Time { return t.Add(-time.Second).UTC().Truncate(time.Hour) }

func sameHour(a, b time.Time) bool { return hourOf(a).Equal(hourOf(b)) }

func (m *Monitor) saveHour(ctx context.Context, h Hour, _ time.Time) {
	if m.keeper == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if st, err := m.keeper.DBStats(sctx); err == nil {
		h.DBBytes = st.Bytes
	}
	if err := m.keeper.SaveHealthHour(sctx, h); err != nil && ctx.Err() == nil {
		m.logger.Warn("saúde do servidor: gravar a hora", "error", err)
	}
	if _, err := m.keeper.PruneHealth(sctx, h.Hour.Add(-HoursKeep)); err != nil && ctx.Err() == nil {
		m.logger.Warn("saúde do servidor: retenção", "error", err)
	}
}

// HoursKeep: as horas ficam no banco por 90 dias.
const HoursKeep = 90 * 24 * time.Hour

// aggregate resume os pontos de uma hora.
func aggregate(pts []Point) Hour {
	h := Hour{Hour: hourOf(pts[0].At), Samples: len(pts), DiskFreeMin: Unknown, HostCPUAvg: Unknown, HostCPUMax: Unknown, HostMemMax: Unknown}
	var cpu, mem, hostCPU, latSum float64
	hostN, timed := 0, 0
	for _, p := range pts {
		cpu += p.CPUPct
		mem += p.MemPct
		h.CPUMax = math.Max(h.CPUMax, p.CPUPct)
		h.MemMax = math.Max(h.MemMax, p.MemPct)
		h.MemBytesMax = max(h.MemBytesMax, p.MemBytes)
		if p.HostCPUPct != Unknown {
			hostCPU += p.HostCPUPct
			hostN++
			h.HostCPUMax = math.Max(h.HostCPUMax, p.HostCPUPct)
		}
		if p.HostMemPct != Unknown {
			h.HostMemMax = math.Max(h.HostMemMax, p.HostMemPct)
		}
		h.Requests += int64(p.Requests)
		h.Slow += int64(p.Slow)
		h.Errors += int64(p.Errors)
		latSum += p.LatAvgMs * float64(p.Timed)
		timed += p.Timed
		h.LatP95Max = math.Max(h.LatP95Max, p.LatP95Ms)
		h.PoolInUseMax = max(h.PoolInUseMax, p.PoolInUse)
		h.PoolMax = max(h.PoolMax, p.PoolMax)
		h.PoolWaits += p.PoolWaits
		if p.DiskFreePct != Unknown && (h.DiskFreeMin == Unknown || p.DiskFreePct < h.DiskFreeMin) {
			h.DiskFreeMin = p.DiskFreePct
		}
		h.Goroutines = max(h.Goroutines, p.Goroutines)
	}
	n := float64(len(pts))
	h.CPUAvg, h.MemAvg = cpu/n, mem/n
	if hostN > 0 {
		h.HostCPUAvg = hostCPU / float64(hostN)
	}
	if timed > 0 {
		h.LatAvgMs = latSum / float64(timed)
	}
	return h
}

// Points devolve os minutos desde since (cópia).
func (m *Monitor) Points(since time.Time) []Point {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := sort.Search(len(m.ring), func(i int) bool { return !m.ring[i].At.Before(since) })
	return append([]Point(nil), m.ring[i:]...)
}

// Routes devolve as rotas desde a partida, da mais lenta (média) para a mais rápida.
func (m *Monitor) Routes() []RouteStat {
	m.mu.Lock()
	out := make([]RouteStat, 0, len(m.routes))
	for _, r := range m.routes {
		c := *r
		if c.Count > 0 {
			c.AvgMs = c.sumMs / float64(c.Count)
		}
		out = append(out, c)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].AvgMs != out[j].AvgMs {
			return out[i].AvgMs > out[j].AvgMs
		}
		return out[i].Route < out[j].Route
	})
	return out
}

// Started: quando o monitor (o processo) começou.
func (m *Monitor) Started() time.Time { return m.start }

// Info: a capacidade da máquina e do processo.
func (m *Monitor) Info() Info { return m.sys.info() }

// Keeper: o banco (nil sem ele).
func (m *Monitor) Keeper() Keeper { return m.keeper }

// Disks devolve o espaço das pastas de dados agora, uma vez por volume.
func (m *Monitor) Disks() []Disk {
	var out []Disk
	seen := map[uint64]string{}
	for _, p := range m.paths {
		if p.Dir == "" {
			continue
		}
		d := statDisk(p)
		if d.statOK && d.deviceKnown {
			if other, ok := seen[d.device]; ok {
				d.SameAs = other
				d.statOK = false // não conta duas vezes
			} else {
				seen[d.device] = p.Name
			}
		}
		out = append(out, d)
	}
	return out
}

// AddPoint põe um minuto pronto no anel (os testes do painel; a medição usa Run).
func (m *Monitor) AddPoint(p Point) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ring = append(m.ring, p)
	if len(m.ring) > ringSize {
		m.ring = append(m.ring[:0:0], m.ring[len(m.ring)-ringSize:]...)
	}
}
