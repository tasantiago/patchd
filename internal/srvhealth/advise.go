package srvhealth

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// As regras (docs/saude-servidor.md, seção 3): cada uma olha um recurso nas últimas 24 horas
// (o percentil 95 dos minutos, para um pico isolado não pedir hardware) e no que o banco conta
// agora, e diz o que aumentar e por quê. Os limites ficam aqui, não na tela: são regras de
// dimensionamento, não preferências.

// Níveis.
const (
	LevelOK       = "ok"
	LevelWarn     = "atencao"
	LevelCritical = "critico"
)

// Recursos.
const (
	ResCPU     = "CPU"
	ResMem     = "Memória"
	ResDB      = "Banco"
	ResDisk    = "Disco"
	ResLatency = "Tempo de resposta"
)

// Limites das regras (em %, salvo indicação).
const (
	cpuWarn, cpuCrit         = 70, 85
	hostCPUWarn, hostCPUCrit = 75, 90
	memWarn, memCrit         = 80, 90
	hostMemWarn, hostMemCrit = 90, 95
	poolWaitWarn             = 5  // % das conexões pedidas que esperaram
	poolWaitCrit             = 20 // idem
	poolSatWarn              = 2  // % dos minutos com o pool cheio
	connWarn, connCrit       = 80, 90
	cacheWarn, cacheCrit     = 97, 90
	cacheMinReads            = 10000 // blocos lidos do disco: abaixo disso a taxa ainda não diz nada
	diskWarn, diskCrit       = 20, 10
	daysWarn, daysCrit       = 90, 30
	latWarnMs, latCritMs     = 1000, 3000
	errWarn                  = 1 // % das requisições com 5xx
	// minPoints: com menos minutos que isso, as regras das 24 horas esperam (o servidor acabou de subir).
	minPoints = 30
	// headroomTarget: a CPU que se quer no p95; a folga é quantas vezes a carga de hoje cabe nela.
	headroomTarget = 70
)

// Advice é uma recomendação.
type Advice struct {
	Resource, Level    string
	Title, Why, Action string
}

// Gauge é um recurso no resumo: o valor que conta, a barra e a linha de baixo.
type Gauge struct {
	Resource, Level string
	Value, Sub      string
	Pct             int // a barra, 0..100 (o quanto do recurso está tomado); -1 sem barra
}

// Report é o que a tela mostra no alto.
type Report struct {
	Level    string
	Verdict  string
	Detail   string
	Advice   []Advice
	Gauges   []Gauge
	Minutes  int // minutos medidos nas 24 horas
	Warming  bool
	Capacity string // "a CPU aguenta ~3× a carga de hoje"
	Growth   string // "o banco cresce 120 MB por dia"
}

// Input é o que as regras olham.
type Input struct {
	Now    time.Time
	Info   Info
	Points []Point // as 24 horas, por minuto
	Hours  []Hour  // os dias anteriores (pode ser nil: sem banco)
	DB     *DBStats
	Pool   PoolStat
	Disks  []Disk
}

// percentile: o p-ésimo percentil (0..100) de vs (vs é alterado: ordenado).
func percentile(vs []float64, p float64) float64 {
	if len(vs) == 0 {
		return math.NaN()
	}
	sort.Float64s(vs)
	i := int(math.Ceil(p/100*float64(len(vs)))) - 1
	return vs[max(0, min(len(vs)-1, i))]
}

func collect(pts []Point, pick func(Point) float64) []float64 {
	out := make([]float64, 0, len(pts))
	for _, p := range pts {
		if v := pick(p); v != Unknown && !math.IsNaN(v) {
			out = append(out, v)
		}
	}
	return out
}

func maxOf(vs []float64) float64 {
	m := math.NaN()
	for _, v := range vs {
		if math.IsNaN(m) || v > m {
			m = v
		}
	}
	return m
}

func levelFor(v, warn, crit float64, lowerIsWorse bool) string {
	if math.IsNaN(v) {
		return LevelOK
	}
	if lowerIsWorse {
		switch {
		case v < crit:
			return LevelCritical
		case v < warn:
			return LevelWarn
		}
		return LevelOK
	}
	switch {
	case v >= crit:
		return LevelCritical
	case v >= warn:
		return LevelWarn
	}
	return LevelOK
}

func worse(a, b string) string {
	rank := map[string]int{LevelOK: 0, LevelWarn: 1, LevelCritical: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Advise aplica as regras.
func Advise(in Input) Report {
	r := Report{Level: LevelOK, Minutes: len(in.Points)}
	r.Warming = len(in.Points) < minPoints
	add := func(a Advice) {
		r.Advice = append(r.Advice, a)
		r.Level = worse(r.Level, a.Level)
	}
	cores := coresLabel(in.Info)

	// --- CPU ---------------------------------------------------------------------------
	cpu := collect(in.Points, func(p Point) float64 { return p.CPUPct })
	host := collect(in.Points, func(p Point) float64 { return p.HostCPUPct })
	cpuMax, hostMax := maxOf(cpu), maxOf(host)
	cpu95, host95 := percentile(cpu, 95), percentile(host, 95)
	cpuLevel, hostLevel := LevelOK, LevelOK
	if !r.Warming {
		cpuLevel = levelFor(cpu95, cpuWarn, cpuCrit, false)
		hostLevel = levelFor(host95, hostCPUWarn, hostCPUCrit, false)
	}
	if cpuLevel != LevelOK {
		action := fmt.Sprintf("Aumente a CPU do servidor (hoje %s). Com o dobro, o p95 cai para perto de %s.", cores, pctText(cpu95/2))
		if in.Info.CPUQuota > 0 && in.Info.CPUQuota < float64(in.Info.Cores) {
			action = fmt.Sprintf("Aumente a cota de CPU do container (hoje %s; a máquina tem %d núcleos): no compose, \"cpus:\" do serviço server.",
				trimFloat(in.Info.CPUQuota)+" núcleos", in.Info.Cores)
		}
		add(Advice{Resource: ResCPU, Level: cpuLevel, Title: "A CPU do patchd está no limite",
			Why:    fmt.Sprintf("Em 5%% dos minutos das últimas 24 horas o patchd usou %s ou mais da CPU que tem (pico %s).", pctText(cpu95), pctText(cpuMax)),
			Action: action})
	}
	if hostLevel != LevelOK && (math.IsNaN(cpu95) || cpu95 < cpuWarn) {
		add(Advice{Resource: ResCPU, Level: hostLevel, Title: "A máquina está ocupada por outros processos",
			Why: fmt.Sprintf("A CPU da máquina inteira ficou em %s ou mais em 5%% dos minutos (pico %s), mas o patchd usou só %s: quem gasta é outro processo, em geral o PostgreSQL na mesma máquina.",
				pctText(host95), pctText(hostMax), pctText(cpu95)),
			Action: "Aumente a CPU da máquina, ou leve o PostgreSQL para outra; se o banco é o culpado, veja as consultas lentas no PostgreSQL (pg_stat_statements)."})
	}
	g := Gauge{Resource: ResCPU, Level: worse(cpuLevel, hostLevel), Pct: -1}
	switch {
	case len(cpu) == 0:
		g.Value, g.Sub = "—", "não medida neste sistema"
	default:
		g.Value = pctText(lastOf(in.Points, func(p Point) float64 { return p.CPUPct }))
		g.Pct = pctInt(cpu95)
		sub := []string{"p95 " + pctText(cpu95), "pico " + pctText(cpuMax), cores}
		if len(host) > 0 {
			sub = append(sub, "máquina p95 "+pctText(host95))
		}
		g.Sub = strings.Join(sub, " · ")
	}
	// A carga que ainda cabe: quantas vezes o p95 de hoje cabe em headroomTarget.
	if !r.Warming && !math.IsNaN(cpu95) {
		if cpu95 < 1 {
			r.Capacity = "A CPU está praticamente parada: cabe muitas vezes a carga de hoje."
		} else if f := headroomTarget / cpu95; f >= 1.2 {
			r.Capacity = fmt.Sprintf("A CPU aguenta cerca de %s a carga de hoje antes de chegar a %d%%.", timesText(f), headroomTarget)
		}
	}
	r.Gauges = append(r.Gauges, g)

	// --- Memória -----------------------------------------------------------------------
	memPts := collect(in.Points, func(p Point) float64 { return p.MemPct })
	mem95, memMax := percentile(memPts, 95), maxOf(memPts)
	hostMem := collect(in.Points, func(p Point) float64 { return p.HostMemPct })
	hostMem95, hostMemMax := percentile(hostMem, 95), maxOf(hostMem)
	memLevel, hostMemLevel := LevelOK, LevelOK
	if !r.Warming {
		memLevel = levelFor(mem95, memWarn, memCrit, false)
		hostMemLevel = worse(levelFor(hostMem95, hostMemWarn, hostMemCrit, false), levelFor(hostMemMax, 101, hostMemCrit, false))
	}
	var lastMem uint64
	if n := len(in.Points); n > 0 {
		lastMem = in.Points[n-1].MemBytes
	}
	if memLevel != LevelOK {
		where := "da máquina"
		if in.Info.MemFromCG {
			where = "do container (no compose, \"mem_limit:\" do serviço server)"
		}
		want := uint64(float64(maxMemBytes(in.Points)) * 1.5)
		add(Advice{Resource: ResMem, Level: memLevel, Title: "A memória do patchd está perto do limite",
			Why: fmt.Sprintf("Em 5%% dos minutos o patchd usou %s ou mais do limite de %s (pico %s). No limite, o sistema mata o processo.",
				pctText(mem95), Bytes(in.Info.MemLimit), pctText(memMax)),
			Action: fmt.Sprintf("Aumente a memória %s para pelo menos %s (1,5× o pico).", where, Bytes(want))})
	}
	if hostMemLevel != LevelOK {
		add(Advice{Resource: ResMem, Level: hostMemLevel, Title: "A máquina está sem memória livre",
			Why: fmt.Sprintf("A memória da máquina inteira chegou a %s em uso (p95 %s). O PostgreSQL e o cache de disco usam a mesma memória: com pouca sobra, o banco lê mais do disco e tudo fica lento.",
				pctText(hostMemMax), pctText(hostMem95)),
			Action: fmt.Sprintf("Aumente a memória da máquina (hoje %s).", Bytes(in.Info.HostMemTotal))})
	}
	g = Gauge{Resource: ResMem, Level: worse(memLevel, hostMemLevel), Pct: -1}
	switch {
	case len(memPts) == 0:
		g.Value = Bytes(lastMem)
		g.Sub = "sem limite conhecido"
	default:
		g.Value = pctText(lastOf(in.Points, func(p Point) float64 { return p.MemPct }))
		g.Pct = pctInt(mem95)
		limit := "da máquina"
		if in.Info.MemFromCG {
			limit = "do container"
		}
		sub := []string{Bytes(lastMem) + " de " + Bytes(in.Info.MemLimit) + " " + limit, "pico " + pctText(memMax)}
		if len(hostMem) > 0 {
			sub = append(sub, "máquina p95 "+pctText(hostMem95))
		}
		g.Sub = strings.Join(sub, " · ")
	}
	r.Gauges = append(r.Gauges, g)

	// --- Banco -------------------------------------------------------------------------
	var poolFired bool
	if in.Pool.Max > 0 || in.DB != nil {
		g = Gauge{Resource: ResDB, Level: LevelOK, Pct: -1}
		var acquires, waits int64
		var waitMs float64
		saturated := 0
		withPool := 0
		peak := int32(0)
		for _, p := range in.Points {
			if p.PoolMax <= 0 {
				continue
			}
			withPool++
			acquires += p.PoolAcquires
			waits += p.PoolWaits
			waitMs += p.PoolWaitMs
			peak = max(peak, p.PoolInUse)
			if p.PoolInUse >= p.PoolMax {
				saturated++
			}
		}
		if !r.Warming && acquires >= 100 && withPool > 0 {
			waitPct := 100 * float64(waits) / float64(acquires)
			satPct := 100 * float64(saturated) / float64(withPool)
			avgWait := 0.0
			if waits > 0 {
				avgWait = waitMs / float64(waits)
			}
			lvl := LevelOK
			switch {
			case waitPct >= poolWaitCrit || (waitPct >= poolWaitWarn && avgWait >= 100):
				lvl = LevelCritical
			case waitPct >= poolWaitWarn || satPct >= poolSatWarn:
				lvl = LevelWarn
			}
			if lvl != LevelOK {
				poolFired = true
				room := ""
				if in.DB != nil && in.DB.MaxConnections > 0 {
					room = fmt.Sprintf(", sem passar do max_connections do PostgreSQL (%d) menos as outras conexões", in.DB.MaxConnections)
				}
				add(Advice{Resource: ResDB, Level: lvl, Title: "Requisições esperando conexão com o banco",
					Why: fmt.Sprintf("%s das conexões pedidas nas últimas 24 horas esperaram o pool (em média %s cada); o pool ficou cheio (%d de %d) em %s dos minutos.",
						pctText(waitPct), msText(avgWait), peak, in.Pool.Max, pctText(satPct)),
					Action: fmt.Sprintf("Aumente pool_max_conns na PATCHD_DATABASE_URL (hoje %d; tente %d)%s. Se a CPU da máquina do banco também está alta, aumente a CPU antes: mais conexões não aceleram um banco sem CPU.",
						in.Pool.Max, in.Pool.Max*2, room)})
				g.Level = worse(g.Level, lvl)
			}
		}
		sub := []string{}
		if in.Pool.Max > 0 {
			g.Value = fmt.Sprintf("%d de %d", peak, in.Pool.Max)
			g.Pct = pctInt(100 * float64(peak) / float64(in.Pool.Max))
			sub = append(sub, "pico do pool nas 24 h")
		}
		if db := in.DB; db != nil {
			if db.MaxConnections > 0 {
				connPct := 100 * float64(db.Connections) / float64(db.MaxConnections)
				lvl := levelFor(connPct, connWarn, connCrit, false)
				if lvl != LevelOK {
					add(Advice{Resource: ResDB, Level: lvl, Title: "O PostgreSQL está perto do máximo de conexões",
						Why:    fmt.Sprintf("%d de %d conexões abertas (%s), contando todos os clientes do banco.", db.Connections, db.MaxConnections, pctText(connPct)),
						Action: "Aumente max_connections no PostgreSQL (e a memória do banco junto: cada conexão gasta alguns MB), ou diminua pool_max_conns de outros clientes."})
					g.Level = worse(g.Level, lvl)
				}
				sub = append(sub, fmt.Sprintf("%d de %d conexões no PostgreSQL", db.Connections, db.MaxConnections))
			}
			if db.CacheHitPct != Unknown && db.BlocksRead >= cacheMinReads {
				lvl := levelFor(db.CacheHitPct, cacheWarn, cacheCrit, true)
				if lvl != LevelOK {
					add(Advice{Resource: ResDB, Level: lvl, Title: "O banco lê demais do disco",
						Why:    fmt.Sprintf("Só %s das leituras do PostgreSQL vieram da memória (o esperado é 99%%); o banco tem %s.", pctText(db.CacheHitPct), Bytes(uint64(max(0, db.Bytes)))),
						Action: "Aumente shared_buffers no PostgreSQL (perto de 25% da memória da máquina do banco) e a memória dessa máquina, para as tabelas mais usadas caberem no cache."})
					g.Level = worse(g.Level, lvl)
				}
				sub = append(sub, "cache "+pctText(db.CacheHitPct))
			}
			if g.Value == "" {
				g.Value = Bytes(uint64(max(0, db.Bytes)))
			} else {
				sub = append(sub, Bytes(uint64(max(0, db.Bytes))))
			}
		}
		g.Sub = strings.Join(sub, " · ")
		r.Gauges = append(r.Gauges, g)
	}

	// --- Disco -------------------------------------------------------------------------
	growth := dbGrowthPerDay(in.Hours, in.DB, in.Now)
	if growth > 0 {
		r.Growth = fmt.Sprintf("O banco cresce cerca de %s por dia (últimos 7 dias).", Bytes(uint64(growth)))
	}
	g = Gauge{Resource: ResDisk, Level: LevelOK, Pct: -1}
	var worst *Disk
	for i := range in.Disks {
		d := &in.Disks[i]
		if !d.Measured() {
			continue
		}
		if worst == nil || d.FreePct < worst.FreePct {
			worst = d
		}
		lvl := levelFor(d.FreePct, diskWarn, diskCrit, true)
		if lvl != LevelOK {
			add(Advice{Resource: ResDisk, Level: lvl, Title: "Pouco espaço em " + d.Name,
				Why:    fmt.Sprintf("%s livres de %s (%s) em %s.", Bytes(d.Free), Bytes(d.Total), pctText(d.FreePct), d.Dir),
				Action: "Aumente o disco desse volume (ou libere espaço: versões antigas do agente, cache de repositórios)."})
			g.Level = worse(g.Level, lvl)
		}
	}
	if worst != nil {
		g.Value = pctText(worst.FreePct) + " livre"
		g.Pct = pctInt(100 - worst.FreePct)
		sub := []string{Bytes(worst.Free) + " em " + worst.Name}
		if growth > 0 {
			days := float64(worst.Free) / growth
			lvl := levelFor(days, daysWarn, daysCrit, true)
			sub = append(sub, "cheio em ~"+daysText(days)+" no ritmo do banco")
			if lvl != LevelOK {
				add(Advice{Resource: ResDisk, Level: lvl, Title: "O disco enche em " + daysText(days),
					Why: fmt.Sprintf("O banco cresce %s por dia e restam %s em %s. A conta vale se o PostgreSQL grava no mesmo disco (o volume pgdata do compose fica na mesma máquina).",
						Bytes(uint64(growth)), Bytes(worst.Free), worst.Name),
					Action: "Aumente o disco antes disso, ou encurte a retenção (Configuração › Parâmetros › Retenção) para o banco crescer menos."})
				g.Level = worse(g.Level, lvl)
			}
		}
		g.Sub = strings.Join(sub, " · ")
	} else {
		g.Value, g.Sub = "—", "nenhuma pasta de dados medida"
	}
	r.Gauges = append(r.Gauges, g)

	// --- Tempo de resposta --------------------------------------------------------------
	var lat []float64
	var reqs, errs int64
	for _, p := range in.Points {
		reqs += int64(p.Requests)
		errs += int64(p.Errors)
		if p.Timed > 0 {
			lat = append(lat, p.LatP95Ms)
		}
	}
	g = Gauge{Resource: ResLatency, Level: LevelOK, Pct: -1}
	if len(lat) > 0 {
		lat95 := percentile(lat, 95)
		lvl := LevelOK
		if !r.Warming {
			lvl = levelFor(lat95, latWarnMs, latCritMs, false)
		}
		if lvl != LevelOK {
			cause := "Nem a CPU nem o banco estão no limite: veja as rotas mais lentas abaixo (uma tela ou um relatório pesado) e o log do servidor."
			switch {
			case cpuLevel != LevelOK:
				cause = "A CPU está no limite (acima): aumentar a CPU resolve as duas coisas."
			case poolFired:
				cause = "As requisições esperam conexão com o banco (acima): aumentar o pool resolve as duas coisas."
			case hostLevel != LevelOK:
				cause = "A máquina está ocupada por outros processos (acima)."
			}
			add(Advice{Resource: ResLatency, Level: lvl, Title: "O servidor está lento para responder",
				Why:    fmt.Sprintf("Em 5%% dos minutos, as requisições mais lentas passaram de %s.", msText(lat95)),
				Action: cause})
			g.Level = lvl
		}
		perMin := float64(reqs) / float64(max(1, len(in.Points)))
		g.Value = msText(lat95)
		g.Sub = fmt.Sprintf("p95 nas 24 h · %s requisições por minuto", trimFloat(math.Round(perMin*10)/10))
		g.Pct = pctInt(100 * lat95 / latCritMs)
	} else {
		g.Value, g.Sub = "—", "nenhuma requisição medida ainda"
	}
	if !r.Warming && reqs >= 1000 {
		if ep := 100 * float64(errs) / float64(reqs); ep >= errWarn {
			add(Advice{Resource: ResLatency, Level: LevelWarn, Title: "Erros do servidor nas respostas",
				Why:    fmt.Sprintf("%s das requisições das últimas 24 horas terminaram em erro 5xx (%d).", pctText(ep), errs),
				Action: "Não é falta de recurso por si só: veja o log do servidor (nível error) para a causa."})
			g.Level = worse(g.Level, LevelWarn)
		}
	}
	r.Gauges = append(r.Gauges, g)

	// --- O veredito --------------------------------------------------------------------
	sort.SliceStable(r.Advice, func(i, j int) bool {
		return r.Advice[i].Level == LevelCritical && r.Advice[j].Level != LevelCritical
	})
	var crit, warn []string
	for _, a := range r.Advice {
		switch a.Level {
		case LevelCritical:
			crit = appendOnce(crit, strings.ToLower(a.Resource))
		case LevelWarn:
			warn = appendOnce(warn, strings.ToLower(a.Resource))
		}
	}
	switch {
	case len(crit) > 0:
		r.Verdict = "Aumente agora: " + joinPT(crit)
	case len(warn) > 0:
		r.Verdict = "Fique de olho: " + joinPT(warn)
	case r.Warming:
		r.Verdict = "Medindo"
		r.Detail = fmt.Sprintf("O servidor subiu há pouco: %d de %d minutos medidos antes das regras das 24 horas valerem. Disco e banco já são conferidos.", len(in.Points), minPoints)
	default:
		r.Verdict = "Nada a aumentar agora"
		r.Detail = "CPU, memória, banco, disco e tempo de resposta estão dentro da folga nas últimas 24 horas."
	}
	return r
}

func appendOnce(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

func joinPT(list []string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " e " + list[len(list)-1]
}

// lastOf: o valor mais recente conhecido.
func lastOf(pts []Point, pick func(Point) float64) float64 {
	for i := len(pts) - 1; i >= 0; i-- {
		if v := pick(pts[i]); v != Unknown {
			return v
		}
	}
	return Unknown
}

func maxMemBytes(pts []Point) uint64 {
	var m uint64
	for _, p := range pts {
		m = max(m, p.MemBytes)
	}
	return m
}

// dbGrowthPerDay: o crescimento do banco por dia, pela hora mais antiga dos últimos 7 dias e o
// tamanho de agora (0: sem dados bastantes, ou o banco encolheu).
func dbGrowthPerDay(hours []Hour, db *DBStats, now time.Time) float64 {
	var first *Hour
	for i := range hours {
		h := &hours[i]
		if h.DBBytes <= 0 || h.Hour.Before(now.Add(-7*24*time.Hour)) {
			continue
		}
		if first == nil || h.Hour.Before(first.Hour) {
			first = h
		}
	}
	if first == nil {
		return 0
	}
	lastBytes, lastAt := int64(0), time.Time{}
	if db != nil && db.Bytes > 0 {
		lastBytes, lastAt = db.Bytes, now
	} else {
		for i := range hours {
			if hours[i].DBBytes > 0 && hours[i].Hour.After(lastAt) {
				lastBytes, lastAt = hours[i].DBBytes, hours[i].Hour
			}
		}
	}
	span := lastAt.Sub(first.Hour)
	if span < 24*time.Hour || lastBytes <= first.DBBytes {
		return 0
	}
	return float64(lastBytes-first.DBBytes) / span.Hours() * 24
}

// --- Textos ----------------------------------------------------------------------------

func coresLabel(in Info) string {
	if in.CPUQuota > 0 && in.CPUQuota < float64(in.Cores) {
		return trimFloat(in.CPUQuota) + " núcleos (cota do container)"
	}
	if in.Cores == 1 {
		return "1 núcleo"
	}
	return fmt.Sprintf("%d núcleos", in.Cores)
}

func pctInt(v float64) int {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return int(math.Min(100, math.Round(v)))
}

func pctText(v float64) string {
	if math.IsNaN(v) || v == Unknown {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", v)
}

// PctText é o texto de uma porcentagem (— para Unknown).
func PctText(v float64) string { return pctText(v) }

func msText(ms float64) string {
	switch {
	case math.IsNaN(ms):
		return "—"
	case ms >= 1000:
		return strings.Replace(fmt.Sprintf("%.1f s", ms/1000), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// MsText é o texto de um tempo em milissegundos.
func MsText(ms float64) string { return msText(ms) }

func timesText(f float64) string {
	if f >= 10 {
		return "10× ou mais"
	}
	return strings.Replace(fmt.Sprintf("%.1f×", f), ".", ",", 1)
}

func daysText(d float64) string {
	switch {
	case d >= 365:
		return "mais de um ano"
	case d < 1:
		return "menos de um dia"
	case math.Round(d) == 1:
		return "1 dia"
	}
	return fmt.Sprintf("%.0f dias", d)
}

func trimFloat(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
	return strings.Replace(s, ".", ",", 1)
}

// Bytes escreve um tamanho em unidades de 1024 (como o df -h).
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := fmt.Sprintf("%.1f", v)
	if v >= 100 {
		s = fmt.Sprintf("%.0f", v)
	}
	return strings.Replace(s, ".", ",", 1) + " " + []string{"KB", "MB", "GB", "TB", "PB"}[exp]
}

func levelText(l string) string {
	switch l {
	case LevelCritical:
		return "aumentar"
	case LevelWarn:
		return "atenção"
	}
	return "ok"
}

// LevelText é o nível em palavra (a cor não é a única pista).
func (a Advice) LevelText() string { return levelText(a.Level) }

// LevelText é o nível em palavra.
func (g Gauge) LevelText() string { return levelText(g.Level) }
