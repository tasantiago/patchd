package api

import (
	"fmt"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/srvhealth"
)

// A aba Saúde da Configuração (docs/saude-servidor.md): o que o servidor mede de si mesmo e o
// que aumentar. Só leitura; nada de segredo nem de endereço do banco.

// WithServerHealth liga a aba Saúde e a contagem das requisições.
func WithServerHealth(m *srvhealth.Monitor) Option {
	return func(a *api) { a.health = m }
}

type healthView struct {
	Report    srvhealth.Report
	Periods   []perfPeriod
	Hourly    bool
	Charts    []*perfChart
	NoHistory bool // sem banco: só as 24 horas
	Since     string
	Routes    []healthRoute
	RoutesAll int
	DB        []settingRow
	Tables    []healthTable
	Disks     []healthDisk
	Process   []settingRow
}

type healthRoute struct {
	Route, Count, Avg, Max, Share string
	Slow, Errors                  int64
	Level                         string
}

type healthTable struct{ Name, Size string }

type healthDisk struct {
	Name, Dir, Free, Total, Pct, Level, Note string
}

// healthPeriods: os períodos dos gráficos (as 24 horas vêm da memória; o resto, do banco).
var healthPeriods = []struct {
	Key, Label string
	Back       time.Duration
}{{"24h", "24 horas", 24 * time.Hour}, {"7d", "7 dias", 7 * 24 * time.Hour}, {"30d", "30 dias", 30 * 24 * time.Hour}, {"90d", "90 dias", 90 * 24 * time.Hour}}

// healthRoutes: as rotas mostradas (as que mais tomam tempo do servidor).
const healthRoutes = 12

func (a *api) healthTab(r *http.Request) (*healthView, error) {
	ctx := r.Context()
	m := a.health
	now := a.now()
	v := &healthView{Since: a.stamp(m.Started()) + " (" + ago(now.Sub(m.Started())) + ")"}
	k := m.Keeper()
	v.NoHistory = k == nil

	key := r.URL.Query().Get("periodo")
	back := healthPeriods[0].Back
	found := false
	for _, p := range healthPeriods {
		if k == nil && p.Key != "24h" {
			continue
		}
		active := p.Key == key
		if active {
			back, found = p.Back, true
		}
		v.Periods = append(v.Periods, perfPeriod{Key: p.Key, Label: p.Label, Href: "/painel/configuracao?aba=saude&periodo=" + p.Key, Active: active})
	}
	if !found {
		key, v.Periods[0].Active = healthPeriods[0].Key, true
	}

	pts := m.Points(now.Add(-24 * time.Hour))
	in := srvhealth.Input{Now: now, Info: m.Info(), Points: pts, Disks: m.Disks()}
	var hours []srvhealth.Hour
	if k != nil {
		in.Pool = k.PoolStat()
		db, err := k.DBStats(ctx)
		if err != nil {
			return nil, err
		}
		in.DB = &db
		from := now.Add(-7 * 24 * time.Hour)
		if key != "24h" {
			from = now.Add(-back)
		}
		if hours, err = k.HealthHours(ctx, from); err != nil {
			return nil, err
		}
		in.Hours = hours
		for _, t := range db.Tables {
			v.Tables = append(v.Tables, healthTable{Name: t.Name, Size: srvhealth.Bytes(uint64(max(0, t.Bytes)))}) // #nosec G115 -- não negativo
		}
	}
	v.Report = srvhealth.Advise(in)
	if in.DB != nil {
		v.DB = a.healthDBRows(*in.DB, in.Pool, v.Report.Growth)
	}

	loc := time.UTC
	if l, err := time.LoadLocation(a.tz()); a.tz() != "" && err == nil {
		loc = l
	}
	if key == "24h" {
		v.Charts = minuteCharts(pts, now.Add(-24*time.Hour), now, loc, k != nil)
	} else {
		v.Hourly = true
		v.Charts = hourCharts(hours, now.Add(-back), now, loc)
	}
	v.Routes, v.RoutesAll = routeRows(m.Routes())
	v.Disks = diskRows(in.Disks)
	v.Process = processRows(in.Info, pts)
	return v, nil
}

func (a *api) healthDBRows(db srvhealth.DBStats, pool srvhealth.PoolStat, growth string) []settingRow {
	rows := []settingRow{{Label: "Tamanho", Value: srvhealth.Bytes(uint64(max(0, db.Bytes)))}} // #nosec G115 -- não negativo
	if growth != "" {
		rows[0].Value += " · " + growth
	}
	rows = append(rows, settingRow{Label: "Pool deste servidor", Value: fmt.Sprintf("%d em uso de %d (%d abertas) · %d esperas por conexão desde a partida", pool.InUse, pool.Max, pool.Total, pool.Waits)})
	if db.MaxConnections > 0 {
		rows = append(rows, settingRow{Label: "Conexões no PostgreSQL", Value: fmt.Sprintf("%d de %d (max_connections), de todos os clientes", db.Connections, db.MaxConnections)})
	}
	if db.CacheHitPct != srvhealth.Unknown {
		lvl := ""
		if db.CacheHitPct < 97 && db.BlocksRead >= 10000 {
			lvl = "atencao"
		}
		rows = append(rows, settingRow{Label: "Leituras da memória", Value: decimal(db.CacheHitPct, 2) + "% (o esperado é 99% ou mais; o resto vem do disco)", Level: lvl})
	}
	if db.LongestQuery > 0 {
		lvl := ""
		if db.LongestQuery > 5*time.Minute {
			lvl = "atencao"
		}
		rows = append(rows, settingRow{Label: "Consulta ativa mais longa", Value: db.LongestQuery.Round(time.Second).String(), Level: lvl})
	}
	rows = append(rows, settingRow{Label: "Arquivos temporários", Value: srvhealth.Bytes(uint64(max(0, db.TempBytes))) + " desde a última limpeza das estatísticas (consultas que não couberam no work_mem)"}) // #nosec G115 -- não negativo
	if db.Deadlocks > 0 {
		rows = append(rows, settingRow{Label: "Deadlocks", Value: strconv.FormatInt(db.Deadlocks, 10), Level: "atencao"})
	}
	return rows
}

func routeRows(all []srvhealth.RouteStat) ([]healthRoute, int) {
	var total float64
	for _, r := range all {
		total += r.AvgMs * float64(r.Count)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].AvgMs*float64(all[i].Count) > all[j].AvgMs*float64(all[j].Count)
	})
	var out []healthRoute
	for i, r := range all {
		if i == healthRoutes {
			break
		}
		row := healthRoute{Route: r.Route, Count: strconv.FormatInt(r.Count, 10), Avg: srvhealth.MsText(r.AvgMs), Max: srvhealth.MsText(r.MaxMs),
			Slow: r.Slow, Errors: r.Errors}
		if total > 0 {
			row.Share = srvhealth.PctText(100 * r.AvgMs * float64(r.Count) / total)
		}
		if r.AvgMs >= srvhealth.SlowMs {
			row.Level = "atencao"
		}
		out = append(out, row)
	}
	return out, len(all)
}

func diskRows(disks []srvhealth.Disk) []healthDisk {
	var out []healthDisk
	for _, d := range disks {
		row := healthDisk{Name: d.Name, Dir: d.Dir}
		switch {
		case d.SameAs != "":
			row.Note = "o mesmo volume de " + d.SameAs
		case d.Err != "":
			row.Note = "não lido: " + d.Err
		default:
			row.Free, row.Total, row.Pct = srvhealth.Bytes(d.Free), srvhealth.Bytes(d.Total), srvhealth.PctText(d.FreePct)
			switch {
			case d.FreePct < 10:
				row.Level = "erro"
			case d.FreePct < 20:
				row.Level = "atencao"
			}
		}
		out = append(out, row)
	}
	return out
}

func processRows(in srvhealth.Info, pts []srvhealth.Point) []settingRow {
	cores := fmt.Sprintf("%d núcleos na máquina · GOMAXPROCS %d", in.Cores, in.GOMAXPROCS)
	if in.CPUQuota > 0 {
		cores += " · cota do container " + decimal(in.CPUQuota, -1) + " núcleos"
	}
	rows := []settingRow{{Label: "CPU", Value: cores}}
	mem := "limite " + srvhealth.Bytes(in.MemLimit)
	switch {
	case in.MemLimit == 0:
		mem = "limite desconhecido neste sistema"
	case in.MemFromCG:
		mem += " (do container)"
	default:
		mem += " (a memória da máquina; o container não tem limite)"
	}
	if in.HostMemTotal > 0 && in.MemFromCG {
		mem += " · a máquina tem " + srvhealth.Bytes(in.HostMemTotal)
	}
	if in.GoMemLimit > 0 && in.GoMemLimit < math.MaxInt64 {
		mem += " · limite brando do Go " + srvhealth.Bytes(uint64(in.GoMemLimit)) // #nosec G115 -- positivo
	}
	rows = append(rows, settingRow{Label: "Memória", Value: mem})
	if n := len(pts); n > 0 {
		p := pts[n-1]
		rows = append(rows, settingRow{Label: "Go", Value: fmt.Sprintf("%s · heap %s · %d goroutines · GC %s no último minuto", runtime.Version(),
			srvhealth.Bytes(p.HeapBytes), p.Goroutines, srvhealth.MsText(p.GCPauseMs))})
		if p.Load1 != srvhealth.Unknown {
			lvl := ""
			if in.Cores > 0 && p.Load1 > float64(in.Cores) {
				lvl = "atencao"
			}
			rows = append(rows, settingRow{Label: "Carga da máquina", Value: decimal(p.Load1, 2) + " (1 minuto; acima do número de núcleos, há fila para a CPU)", Level: lvl})
		}
	}
	return rows
}

// --- Os gráficos ----------------------------------------------------------------------

// niceTop: o topo do eixo, um número redondo (1, 2, 2,5 ou 5 vezes uma potência de 10) acima
// do maior valor, e nunca menor que floor.
func niceTop(v, floor float64) float64 {
	v = math.Max(v*1.1, floor)
	if v <= 0 {
		return floor
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

func peakOf(series ...[]valPt) float64 {
	m := 0.0
	for _, s := range series {
		for _, p := range s {
			m = math.Max(m, math.Max(p.V, p.Max))
		}
	}
	return m
}

func countLabel(v float64) string { return strconv.FormatFloat(math.Round(v), 'f', 0, 64) }

func sizeLabel(v float64) string { return srvhealth.Bytes(uint64(math.Max(0, v))) }

type healthSeries struct {
	cpu, hostCPU, mem, hostMem, pool, lat, req, db, disk []valPt
}

func (s healthSeries) charts(base chartIn, withPool, long bool) []*perfChart {
	var out []*perfChart
	cpu := base
	cpu.Metric, cpu.Title, cpu.Limit = "srv-cpu", "CPU", 70
	cpu.Series = []seriesIn{{Name: "patchd", Pts: s.cpu}}
	if len(s.hostCPU) > 0 {
		cpu.Series = append(cpu.Series, seriesIn{Name: "máquina inteira", Pts: s.hostCPU})
	}
	out = append(out, buildChart(cpu))
	mem := base
	mem.Metric, mem.Title, mem.Limit = "srv-mem", "Memória (do limite)", 80
	mem.Series = []seriesIn{{Name: "patchd", Pts: s.mem}}
	if len(s.hostMem) > 0 {
		mem.Series = append(mem.Series, seriesIn{Name: "máquina inteira", Pts: s.hostMem})
	}
	out = append(out, buildChart(mem))
	if withPool {
		pool := base
		pool.Metric, pool.Title, pool.NowWord = "srv-pool", "Pool do banco (pico de conexões em uso)", "em uso"
		pool.Series = []seriesIn{{Name: "pool", Pts: s.pool}}
		out = append(out, buildChart(pool))
	}
	lat := base
	lat.Metric, lat.Title, lat.Fmt = "srv-lat", "Tempo de resposta (p95)", srvhealth.MsText
	lat.Top = niceTop(peakOf(s.lat), 100)
	if lat.Top >= 1000 {
		lat.Limit = 1000
	}
	lat.Series = []seriesIn{{Name: "p95", Pts: s.lat}}
	out = append(out, buildChart(lat))
	req := base
	req.Metric, req.Title, req.Fmt, req.NowWord = "srv-req", "Requisições por minuto", countLabel, "por minuto"
	req.Top = niceTop(peakOf(s.req), 10)
	req.Series = []seriesIn{{Name: "requisições", Pts: s.req}}
	out = append(out, buildChart(req))
	if long {
		if len(s.db) > 0 {
			db := base
			db.Metric, db.Title, db.Fmt = "srv-db", "Tamanho do banco", sizeLabel
			db.Top = niceTop(peakOf(s.db), 1<<20)
			db.Series = []seriesIn{{Name: "banco", Pts: s.db}}
			out = append(out, buildChart(db))
		}
		if len(s.disk) > 0 {
			disk := base
			disk.Metric, disk.Title, disk.Limit = perfeval.MetricDisk, "Disco das pastas de dados (menor livre)", 20
			disk.Series = []seriesIn{{Name: "livre", Pts: s.disk}}
			out = append(out, buildChart(disk))
		}
	}
	return out
}

func minuteCharts(pts []srvhealth.Point, from, to time.Time, loc *time.Location, withPool bool) []*perfChart {
	var s healthSeries
	one := func(at time.Time, v float64) valPt { return valPt{At: at, V: v, Max: v} }
	for _, p := range pts {
		if p.CPUPct != srvhealth.Unknown {
			s.cpu = append(s.cpu, one(p.At, p.CPUPct))
		}
		if p.HostCPUPct != srvhealth.Unknown {
			s.hostCPU = append(s.hostCPU, one(p.At, p.HostCPUPct))
		}
		if p.MemPct != srvhealth.Unknown {
			s.mem = append(s.mem, one(p.At, p.MemPct))
		}
		if p.HostMemPct != srvhealth.Unknown {
			s.hostMem = append(s.hostMem, one(p.At, p.HostMemPct))
		}
		if v := p.PoolPct(); v != srvhealth.Unknown {
			s.pool = append(s.pool, one(p.At, v))
		}
		if p.Requests > 0 {
			s.lat = append(s.lat, one(p.At, p.LatP95Ms))
		}
		s.req = append(s.req, one(p.At, float64(p.Requests)))
	}
	return s.charts(chartIn{From: from, To: to, Gap: 3 * time.Minute, Loc: loc}, withPool, false)
}

func hourCharts(hours []srvhealth.Hour, from, to time.Time, loc *time.Location) []*perfChart {
	var s healthSeries
	withPool := false
	for _, h := range hours {
		at := h.Hour.Add(30 * time.Minute)
		s.cpu = append(s.cpu, valPt{At: at, V: h.CPUAvg, Max: h.CPUMax})
		if h.HostCPUAvg != srvhealth.Unknown {
			s.hostCPU = append(s.hostCPU, valPt{At: at, V: h.HostCPUAvg, Max: h.HostCPUMax})
		}
		s.mem = append(s.mem, valPt{At: at, V: h.MemAvg, Max: h.MemMax})
		if h.HostMemMax != srvhealth.Unknown {
			s.hostMem = append(s.hostMem, valPt{At: at, V: h.HostMemMax, Max: h.HostMemMax})
		}
		if h.PoolMax > 0 {
			withPool = true
			v := 100 * float64(h.PoolInUseMax) / float64(h.PoolMax)
			s.pool = append(s.pool, valPt{At: at, V: v, Max: v})
		}
		if h.Requests > 0 {
			s.lat = append(s.lat, valPt{At: at, V: h.LatP95Max, Max: h.LatP95Max})
		}
		perMin := float64(h.Requests) / float64(max(1, h.Samples))
		s.req = append(s.req, valPt{At: at, V: perMin, Max: perMin})
		if h.DBBytes > 0 {
			s.db = append(s.db, valPt{At: at, V: float64(h.DBBytes), Max: float64(h.DBBytes)})
		}
		if h.DiskFreeMin != srvhealth.Unknown {
			s.disk = append(s.disk, valPt{At: at, V: h.DiskFreeMin, Max: h.DiskFreeMin})
		}
	}
	return s.charts(chartIn{From: from, To: to, Gap: 2 * time.Hour, Hourly: true, Loc: loc}, withPool, true)
}

// transferRoutes: as rotas cujo tempo não é o do servidor trabalhando, e que ficam fora do
// tempo de resposta da aba Saúde (contam só no volume): os arquivos grandes (o tempo é o da
// rede de quem baixa) e o canal em tempo real, que segura cada pedido até 45 s de propósito
// (internal/canal) e, sozinho, punha o p95 perto de um minuto.
var transferRoutes = map[string]bool{
	"POST " + protocol.ChannelPath:                    true,
	"GET " + protocol.OfflineCatalogPath + "{sha256}": true,
	"GET " + protocol.PackagesPath + "{file}":         true,
	"GET " + protocol.BundlesPath + "{file}":          true,
	"GET /api/v1/agent/releases/{version}/{file}":     true,
	"GET /painel/configuracao/agente/arquivo/{name}":  true,
}

func transferRoute(route string) bool { return transferRoutes[route] }

// decimal: o número com vírgula, como o resto do painel.
func decimal(v float64, prec int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', prec, 64), ".", ",", 1)
}
