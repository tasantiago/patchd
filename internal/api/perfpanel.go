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
	Closed                        string // fechado à mão: por quem e a nota
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
	// Batch: o admin vê as caixas e o formulário do lote; InFilter: os abertos do filtro.
	Batch    bool
	InFilter int
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
		case al.ClosedAt != nil && al.CloseBy != "":
			end = *al.ClosedAt
			r.State, r.Level = "fechado à mão em "+a.stamp(*al.ClosedAt), "ok"
			r.Closed = "por " + al.CloseBy
			if al.CloseNote != "" {
				r.Closed += ": " + al.CloseNote
			}
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
	case q.Get("erro") == "lote-vazio":
		v.Error = "Nenhum alerta para o lote: marque ao menos um aberto (no reconhecer, um ainda sem reconhecimento), ou escolha todos os abertos."
	case q.Has("fechados"):
		v.Flash = fmt.Sprintf("%s alerta(s) fechado(s) à mão. Se a medida continuar além do limite, um alerta novo abre depois dos minutos seguidos.", numOnly(q.Get("fechados")))
	case q.Has("reconhecidos"):
		v.Flash = fmt.Sprintf("%s alerta(s) reconhecido(s): saem do contador do menu e continuam abertos até a medida voltar ao normal.", numOnly(q.Get("reconhecidos")))
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
	if v.Admin {
		open, err := a.store.PerfAlerts(ctx, store.PerfAlertFilter{OpenOnly: true, Limit: maxAlertBatch})
		if err != nil {
			a.internalError(w, r, "contar alertas", err)
			return
		}
		for _, al := range open {
			if v.Metric == "" || al.Metric == v.Metric {
				v.InFilter++
			}
		}
		v.Batch = v.InFilter > 0
	}
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
	Charts   []*perfChart // CPU, memória e disco livre
	Table    []perfTableRow
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
	loc := time.UTC
	if l, err := time.LoadLocation(a.tz()); a.tz() != "" && err == nil {
		loc = l
	}
	var cpu, mem []valPt
	disks := map[string][]valPt{}
	if key == "24h" {
		for _, s := range recent {
			cpu = append(cpu, valPt{At: s.At, V: s.CPU, Max: s.CPU})
			mem = append(mem, valPt{At: s.At, V: s.Mem, Max: s.Mem})
			for m, v := range s.Disks {
				disks[m] = append(disks[m], valPt{At: s.At, V: v, Max: v})
			}
		}
	} else {
		c.Hourly = true
		hours, err := a.store.PerfHourly(ctx, id, from)
		if err != nil {
			return nil, err
		}
		for _, h := range hours {
			at := h.Hour.Add(30 * time.Minute)
			cpu = append(cpu, valPt{At: at, V: h.CPUAvg, Max: h.CPUMax})
			mem = append(mem, valPt{At: at, V: h.MemAvg, Max: h.MemMax})
			if h.DiskMin != nil {
				disks[diskWorst] = append(disks[diskWorst], valPt{At: at, V: *h.DiskMin, Max: *h.DiskMin})
			}
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
	alerts, err := a.store.PerfAlerts(ctx, store.PerfAlertFilter{MachineID: id, Limit: 20})
	if err != nil {
		return nil, err
	}
	c.Alerts = a.alertRows(alerts, nil, admin)
	spans := map[string][]timeSpan{}
	for _, al := range alerts {
		end := now
		if al.ClosedAt != nil {
			end = *al.ClosedAt
		}
		spans[al.Metric] = append(spans[al.Metric], timeSpan{From: al.OpenedAt, To: end})
	}
	gap := 5 * time.Minute
	if c.Hourly {
		gap = 2 * time.Hour
	}
	// Disco: os volumes com menos espaço livre agora, em ordem de ponto de montagem (a cor
	// segue o volume, não a posição na lista).
	mounts := make([]string, 0, len(disks))
	for m := range disks {
		mounts = append(mounts, m)
	}
	lastFree := func(m string) float64 { l := disks[m]; return l[len(l)-1].V }
	sort.Slice(mounts, func(i, j int) bool { return lastFree(mounts[i]) < lastFree(mounts[j]) })
	more := 0
	if len(mounts) > pcMaxDisks {
		more, mounts = len(mounts)-pcMaxDisks, mounts[:pcMaxDisks]
	}
	sort.Strings(mounts)
	var diskSeries []seriesIn
	for _, m := range mounts {
		diskSeries = append(diskSeries, seriesIn{Name: m, Pts: disks[m]})
	}
	base := chartIn{From: from, To: now, Gap: gap, Hourly: c.Hourly, Loc: loc}
	ins := []chartIn{
		withChart(base, perfeval.MetricCPU, "CPU", p.CPUPct, spans, seriesIn{Name: "CPU", Pts: cpu}),
		withChart(base, perfeval.MetricMem, "Memória", p.MemPct, spans, seriesIn{Name: "Memória", Pts: mem}),
		withChart(base, perfeval.MetricDisk, "Disco livre", p.DiskFreePct, spans, diskSeries...),
	}
	ins[2].MoreVolums = more
	for _, in := range ins {
		if len(in.Series) > 0 {
			ch := buildChart(in)
			if !ch.Empty {
				c.Has = true
			}
			c.Charts = append(c.Charts, ch)
		}
	}
	c.Table = perfTable(ins)
	return c, nil
}

// diskWorst: a série por hora do disco (a tabela por hora guarda só o menor livre entre os volumes).
const diskWorst = "menor livre"

func withChart(base chartIn, metric, title string, limit float64, spans map[string][]timeSpan, series ...seriesIn) chartIn {
	base.Metric, base.Title, base.Limit, base.Alerts, base.Series = metric, title, limit, spans[metric], series
	return base
}
