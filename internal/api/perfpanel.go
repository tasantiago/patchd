package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// O painel do desempenho (docs/monitoramento.md, seção 5): a tela Alertas e a aba Desempenho
// do equipamento, com as curvas desenhadas no servidor em SVG (o painel não tem JavaScript).

// maxAckNote: o tamanho da nota do reconhecimento.
const maxAckNote = 200

type perfAlertRow struct {
	ID                            int64
	MachineID, Short, Name, Label string
	Opened, Duration, Peak, Last  string
	State, Level, Ack             string
	Open, CanAck                  bool
}

type alertsView struct {
	pageBase
	Rows          []perfAlertRow
	All           bool   // fechados também
	Metric        string // "" = todas
	Open, Unacked int
	MaxNote       int
	Policy        string
}

// metricLabel: o nome da medida na tela.
func metricLabel(metric, target string) string {
	switch metric {
	case perfeval.MetricCPU:
		return "CPU"
	case perfeval.MetricMem:
		return "Memória"
	case perfeval.MetricDisk:
		return "Disco " + target
	}
	return metric
}

// metricValue: o valor com a unidade (disco: % livre).
func metricValue(metric string, v float64) string {
	s := strconv.FormatFloat(v, 'f', 0, 64) + "%"
	if metric == perfeval.MetricDisk {
		s += " livre"
	}
	return s
}

// span: a duração em palavras ("35 min", "2 h 10 min", "3 dias").
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "menos de 1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d dias", int(d.Hours()/24))
}

func policyText(p protocol.MonitorPolicy) string {
	return fmt.Sprintf("CPU acima de %.0f%%, memória acima de %.0f%% ou disco com menos de %.0f%% livre por %d minutos seguidos "+
		"(Configuração › Parâmetros › Monitoramento)", p.CPUPct, p.MemPct, p.DiskFreePct, p.Minutes)
}

// alertRows monta as linhas. names: o nome de cada máquina.
func (a *api) alertRows(list []store.PerfAlert, names map[string]string, admin bool) []perfAlertRow {
	now := a.now()
	out := make([]perfAlertRow, 0, len(list))
	for _, al := range list {
		r := perfAlertRow{ID: al.ID, MachineID: al.MachineID, Short: short(al.MachineID), Name: names[al.MachineID],
			Label: metricLabel(al.Metric, al.Target), Opened: a.stamp(al.OpenedAt),
			Peak: metricValue(al.Metric, al.Peak), Last: metricValue(al.Metric, al.Last), Open: al.ClosedAt == nil}
		end := now
		switch {
		case al.ClosedAt != nil:
			end = *al.ClosedAt
			r.State, r.Level = "fechado em "+a.stamp(*al.ClosedAt), "ok"
		case al.AckAt != nil:
			r.State, r.Level = "reconhecido", "atencao"
		default:
			r.State, r.Level = "aberto", "erro"
		}
		if al.AckAt != nil {
			r.Ack = fmt.Sprintf("%s, %s", al.AckBy, a.stamp(*al.AckAt))
			if al.AckNote != "" {
				r.Ack += ": " + al.AckNote
			}
		}
		r.Duration = span(end.Sub(al.OpenedAt))
		r.CanAck = admin && r.Open && al.AckAt == nil
		out = append(out, r)
	}
	return out
}

// machineNames: o nome de cada máquina da frota (as aposentadas ficam com o ID curto).
func (a *api) machineNames(ctx context.Context) (map[string]string, error) {
	list, err := a.store.Machines(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(list))
	for _, m := range list {
		names[m.ID] = m.Hostname
	}
	return names, nil
}

// alertsPage: GET /painel/alertas[?estado=todos][&medida=cpu].
func (a *api) alertsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := alertsView{pageBase: a.base(r, "Alertas", "alertas"), All: q.Get("estado") == "todos", MaxNote: maxAckNote,
		Policy: policyText(a.monitorPolicy())}
	switch m := q.Get("medida"); m {
	case perfeval.MetricCPU, perfeval.MetricMem, perfeval.MetricDisk:
		v.Metric = m
	}
	switch {
	case q.Has("reconhecido"):
		v.Flash = "Alerta reconhecido: sai do contador do menu e continua aberto até a medida voltar ao normal."
	case q.Get("erro") == "reconhecer":
		v.Error = "Esse alerta já foi reconhecido ou fechou; nada mudou."
	case q.Get("erro") == "nota":
		v.Error = fmt.Sprintf("A nota tem no máximo %d caracteres.", maxAckNote)
	}
	ctx := r.Context()
	list, err := a.store.PerfAlerts(ctx, store.PerfAlertFilter{OpenOnly: !v.All, Limit: 500})
	if err != nil {
		a.internalError(w, r, "listar alertas", err)
		return
	}
	if v.Metric != "" {
		kept := list[:0]
		for _, al := range list {
			if al.Metric == v.Metric {
				kept = append(kept, al)
			}
		}
		list = kept
	}
	names, err := a.machineNames(ctx)
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	v.Rows = a.alertRows(list, names, v.Admin)
	if v.Open, v.Unacked, err = a.store.PerfAlertCounts(ctx); err != nil {
		a.internalError(w, r, "contar alertas", err)
		return
	}
	a.render(w, r, http.StatusOK, "alertas", v)
}

// ackAlertAction reconhece um alerta aberto.
func (a *api) ackAlertAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxAckNote || strings.ContainsFunc(note, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		http.Redirect(w, r, "/painel/alertas?erro=nota", http.StatusSeeOther)
		return
	}
	ok, err := a.store.AckPerfAlert(r.Context(), id, s.Username, note, a.now())
	if err != nil {
		a.internalError(w, r, "reconhecer alerta", err)
		return
	}
	if !ok {
		http.Redirect(w, r, "/painel/alertas?erro=reconhecer", http.StatusSeeOther)
		return
	}
	a.audit(r, s, "alerta.reconhecer", strconv.FormatInt(id, 10), "nota: "+note)
	http.Redirect(w, r, "/painel/alertas?reconhecido="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// --- A aba Desempenho -----------------------------------------------------------------

type perfPeriod struct {
	Key, Label, Href string
	Active           bool
}

type perfCard struct {
	Has      bool
	At       string // a última amostra
	CPU, Mem string
	Disks    []perfDisk
	Periods  []perfPeriod
	Chart    *chartView
	Hourly   bool // a curva é a média por hora
	Alerts   []perfAlertRow
	Policy   string
}

type perfDisk struct{ Mount, Free, Level string }

// perfPeriods: os períodos da aba (o padrão é o primeiro).
var perfPeriods = []struct {
	Key, Label string
	Back       time.Duration
}{{"24h", "24 horas", 24 * time.Hour}, {"7d", "7 dias", 7 * 24 * time.Hour}, {"90d", "90 dias", 90 * 24 * time.Hour}}

func (a *api) perfView(r *http.Request, id string, admin bool) (*perfCard, error) {
	ctx := r.Context()
	p := a.monitorPolicy()
	c := &perfCard{Policy: policyText(p)}
	key := r.URL.Query().Get("periodo")
	back := perfPeriods[0].Back
	found := false
	for _, pp := range perfPeriods {
		active := pp.Key == key
		if active {
			back, found = pp.Back, true
		}
		c.Periods = append(c.Periods, perfPeriod{Key: pp.Key, Label: pp.Label, Href: machinePath(id) + "?aba=desempenho&periodo=" + pp.Key, Active: active})
	}
	if !found {
		key, c.Periods[0].Active = perfPeriods[0].Key, true
	}
	now := a.now()
	from := now.Add(-back)
	recent, err := a.store.PerfSamples(ctx, id, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	var pts []chartPoint
	if key == "24h" {
		for _, s := range recent {
			pts = append(pts, chartPoint{At: s.At, CPU: s.CPU, Mem: s.Mem, Disk: minDisk(s.Disks)})
		}
	} else {
		c.Hourly = true
		hours, err := a.store.PerfHourly(ctx, id, from)
		if err != nil {
			return nil, err
		}
		for _, h := range hours {
			pts = append(pts, chartPoint{At: h.Hour.Add(30 * time.Minute), CPU: h.CPUAvg, Mem: h.MemAvg, Disk: h.DiskMin})
		}
	}
	if len(recent) > 0 {
		last := recent[len(recent)-1]
		c.Has = true
		c.At = a.stamp(last.At) + " (" + ago(now.Sub(last.At)) + ")"
		c.CPU, c.Mem = metricValue(perfeval.MetricCPU, last.CPU), metricValue(perfeval.MetricMem, last.Mem)
		for m, v := range last.Disks {
			d := perfDisk{Mount: m, Free: metricValue(perfeval.MetricDisk, v)}
			if v < p.DiskFreePct {
				d.Level = "erro"
			}
			c.Disks = append(c.Disks, d)
		}
		sort.Slice(c.Disks, func(i, j int) bool { return c.Disks[i].Mount < c.Disks[j].Mount })
	}
	if len(pts) > 0 {
		c.Has = true
		gap := 5 * time.Minute
		if c.Hourly {
			gap = 2 * time.Hour
		}
		c.Chart = drawChart(pts, from, now, gap, p, a.tz())
	}
	alerts, err := a.store.PerfAlerts(ctx, store.PerfAlertFilter{MachineID: id, Limit: 20})
	if err != nil {
		return nil, err
	}
	c.Alerts = a.alertRows(alerts, nil, admin)
	return c, nil
}

func minDisk(d map[string]float64) *float64 {
	var out *float64
	for _, v := range d {
		if out == nil || v < *out {
			vv := v
			out = &vv
		}
	}
	return out
}

// --- O desenho ----------------------------------------------------------------------

type chartPoint struct {
	At       time.Time
	CPU, Mem float64
	Disk     *float64
}

type chartView struct {
	W, H                int
	CPU, Mem, Disk      []string // segmentos de polyline (um buraco quebra a linha)
	Grid                []chartTick
	Ticks               []chartTick
	Left, Right, Bottom int
	UseLimit, DiskLimit int // y das linhas de limite
	Label               string
}

type chartTick struct {
	X, Y          int
	Label, Anchor string
}

const (
	chartW, chartH       = 720, 200
	chartLeft, chartTop  = 34, 8
	chartRight, chartBot = 712, 182
)

// drawChart desenha as três curvas de 0 a 100% entre from e to.
func drawChart(pts []chartPoint, from, to time.Time, gap time.Duration, p protocol.MonitorPolicy, tz string) *chartView {
	c := &chartView{W: chartW, H: chartH, Left: chartLeft, Right: chartRight, Bottom: chartBot,
		Label: "Uso de CPU, memória e o volume com menos espaço livre, de 0 a 100%"}
	span := to.Sub(from)
	if span <= 0 {
		span = time.Hour
	}
	x := func(t time.Time) int {
		return chartLeft + int(float64(chartRight-chartLeft)*float64(t.Sub(from))/float64(span))
	}
	y := func(v float64) int { return chartBot - int((float64(chartBot-chartTop))*v/100) }
	for _, v := range []float64{0, 25, 50, 75, 100} {
		c.Grid = append(c.Grid, chartTick{Y: y(v), Label: strconv.Itoa(int(v)) + "%"})
	}
	c.UseLimit, c.DiskLimit = y(p.CPUPct), y(p.DiskFreePct)
	loc := time.UTC
	if l, err := time.LoadLocation(tz); tz != "" && err == nil {
		loc = l
	}
	layout := "15:04"
	if span > 48*time.Hour {
		layout = "02/01"
	}
	for i := 0; i <= 4; i++ {
		t := from.Add(span * time.Duration(i) / 4)
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case 4:
			anchor = "end"
		}
		c.Ticks = append(c.Ticks, chartTick{X: x(t), Label: t.In(loc).Format(layout), Anchor: anchor})
	}
	var cpu, mem, disk []string
	flush := func() {
		if len(cpu) > 0 {
			c.CPU, c.Mem = append(c.CPU, strings.Join(cpu, " ")), append(c.Mem, strings.Join(mem, " "))
		}
		if len(disk) > 0 {
			c.Disk = append(c.Disk, strings.Join(disk, " "))
		}
		cpu, mem, disk = nil, nil, nil
	}
	var last time.Time
	for _, pt := range pts {
		if pt.At.Before(from) || pt.At.After(to) {
			continue
		}
		if !last.IsZero() && pt.At.Sub(last) > gap {
			flush()
		}
		last = pt.At
		px := strconv.Itoa(x(pt.At))
		cpu = append(cpu, px+","+strconv.Itoa(y(pt.CPU)))
		mem = append(mem, px+","+strconv.Itoa(y(pt.Mem)))
		if pt.Disk != nil {
			disk = append(disk, px+","+strconv.Itoa(y(*pt.Disk)))
		}
	}
	flush()
	return c
}
