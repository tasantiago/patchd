package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/perfeval"
)

// Os gráficos da aba Desempenho (docs/monitoramento.md, seção 5). Um gráfico por medida
// (CPU e memória sobem quando pioram; disco livre desce), cada um com a área da série, o limite
// rotulado, a faixa dos alertas e o valor atual na ponta. A interação é só SVG e CSS (o painel
// não tem JavaScript): cada intervalo do eixo do tempo é uma zona que, com o mouse em cima,
// mostra a linha-guia e o valor de cada série; a mesma informação está na tabela logo abaixo.

// Geometria (unidades do viewBox; o SVG escala com a largura do cartão).
const (
	pcW, pcH   = 720, 178
	pcL, pcR   = 40, 668 // a sobra à direita é do rótulo do valor atual
	pcT, pcB   = 14, 150
	pcAxisY    = 168
	pcZones    = 96 // zonas de passar o mouse (e linhas da tabela)
	pcTipW     = 150
	pcTipRowH  = 16
	pcMaxDisks = 3 // volumes desenhados (os de menos espaço); o resto fica na tabela
)

// valPt é um ponto de uma série: o valor e, por hora, o pico da hora (por minuto, o próprio valor).
type valPt struct {
	At     time.Time
	V, Max float64
}

type seriesIn struct {
	Name string
	Pts  []valPt
}

type timeSpan struct{ From, To time.Time }

// chartIn é o que o gráfico precisa.
type chartIn struct {
	Metric     string // perfeval.Metric*
	Title      string
	Series     []seriesIn
	From, To   time.Time
	Gap        time.Duration // um buraco maior quebra a linha (máquina desligada)
	Limit      float64
	Alerts     []timeSpan
	Hourly     bool
	Loc        *time.Location
	MoreVolums int // volumes que ficaram de fora do desenho
	// Para medidas fora de 0 a 100% (a saúde do servidor): o topo do eixo, o texto de um valor
	// e a palavra depois do valor atual. Zero: 100, pctLabel e "em uso" (ou "livre" no disco).
	Top     float64
	Fmt     func(float64) string
	NowWord string
}

func (in chartIn) top() float64 {
	if in.Top > 0 {
		return in.Top
	}
	return 100
}

func (in chartIn) y(v float64) int { return pcY(100 * v / in.top()) }

func (in chartIn) label(v float64) string {
	if in.Fmt != nil {
		return in.Fmt(v)
	}
	return pctLabel(v)
}

type perfChart struct {
	ID, Title  string
	Now, Sub   string // o valor atual e a linha de baixo (pico, alertas)
	W, H       int
	L, R, T, B int
	AxisY      int
	AxisX      int // onde terminam os rótulos do eixo dos valores
	PlotH      int
	Grid       []chartTick
	XTicks     []chartTick
	LimitY     int
	LimitLabel string
	Bands      []chartBand
	Series     []chartLine
	Zones      []chartZone
	Legend     bool
	Label      string
	Empty      bool
}

type chartTick struct {
	X, Y          int
	Label, Anchor string
}

type chartBand struct{ X, W int }

type chartLine struct {
	Class, Name  string
	Lines, Areas []string
	EndX, EndY   int
	EndTextX     int
	End          string
	EndLabel     bool
}

type chartZone struct {
	X, W, LineX int
	BoxX, BoxY  int
	BoxW, BoxH  int
	TextX       int
	When        string
	WhenY       int
	Rows        []chartTipRow
	Dots        []chartDot
}

type chartTipRow struct {
	Y            int
	Value, Name  string
	Class        string
	KeyX1, KeyX2 int
	ValueX       int
	NameX        int
}

type chartDot struct {
	X, Y  int
	Class string
}

func pcX(t, from, to time.Time) int {
	span := to.Sub(from)
	if span <= 0 {
		return pcL
	}
	f := float64(t.Sub(from)) / float64(span)
	return pcL + int(math.Round(float64(pcR-pcL)*math.Max(0, math.Min(1, f))))
}

func pcY(v float64) int {
	return pcB - int(math.Round(float64(pcB-pcT)*math.Max(0, math.Min(100, v))/100))
}

func pctLabel(v float64) string { return strconv.FormatFloat(math.Round(v), 'f', 0, 64) + "%" }

// worseOf: o pior de dois valores na medida (disco: o menor).
func worseOf(metric string, a, b float64) float64 {
	if metric == perfeval.MetricDisk {
		return math.Min(a, b)
	}
	return math.Max(a, b)
}

func (in chartIn) when(t time.Time) string {
	t = t.In(in.Loc)
	switch {
	case !in.Hourly:
		return t.Format("02/01 15:04")
	case in.To.Sub(in.From) > 8*24*time.Hour:
		return t.Format("02/01")
	}
	return t.Format("02/01 15h")
}

// buildChart desenha o gráfico.
func buildChart(in chartIn) *perfChart {
	c := &perfChart{ID: in.Metric, Title: in.Title, W: pcW, H: pcH, L: pcL, R: pcR, T: pcT, B: pcB, AxisY: pcAxisY, AxisX: pcL - 8, PlotH: pcB - pcT,
		Legend: len(in.Series) > 1}
	if in.Loc == nil {
		in.Loc = time.UTC
	}
	for _, v := range []float64{0, in.top() / 2, in.top()} {
		c.Grid = append(c.Grid, chartTick{Y: in.y(v), Label: in.label(v)})
	}
	c.XTicks = timeTicks(in.From, in.To, in.Loc)
	if in.Limit > 0 {
		c.LimitY = in.y(in.Limit)
		c.LimitLabel = "limite " + in.label(in.Limit)
		if in.Metric == perfeval.MetricDisk {
			c.LimitLabel += " livre"
		}
	}
	for _, a := range in.Alerts {
		from, to := a.From, a.To
		if to.Before(in.From) || from.After(in.To) {
			continue
		}
		x1, x2 := pcX(from, in.From, in.To), pcX(to, in.From, in.To)
		c.Bands = append(c.Bands, chartBand{X: x1, W: max(2, x2-x1)})
	}

	// As linhas, quebradas nos buracos, e a área de cada uma (só com uma série: áreas
	// sobrepostas viram uma mancha).
	var peak *valPt
	drawn := false
	for i, s := range in.Series {
		ln := chartLine{Class: "s" + strconv.Itoa(i+1), Name: s.Name}
		var seg []string
		var segX []int
		flush := func() {
			if len(seg) == 0 {
				return
			}
			ln.Lines = append(ln.Lines, strings.Join(seg, " "))
			if len(in.Series) == 1 {
				ln.Areas = append(ln.Areas, strings.Join(seg, " ")+fmt.Sprintf(" %d,%d %d,%d", segX[len(segX)-1], pcB, segX[0], pcB))
			}
			seg, segX = nil, nil
		}
		var last time.Time
		for _, p := range s.Pts {
			if p.At.Before(in.From) || p.At.After(in.To) {
				continue
			}
			if !last.IsZero() && p.At.Sub(last) > in.Gap {
				flush()
			}
			last = p.At
			x, y := pcX(p.At, in.From, in.To), in.y(p.V)
			seg = append(seg, strconv.Itoa(x)+","+strconv.Itoa(y))
			segX = append(segX, x)
			ln.EndX, ln.EndY, ln.EndTextX, ln.End = x, y, x+8, in.label(p.V)
			drawn = true
			if len(in.Series) == 1 {
				pk := p
				pk.V = p.Max
				if peak == nil || worseOf(in.Metric, pk.V, peak.V) == pk.V && pk.V != peak.V {
					peak = &pk
				}
			}
		}
		flush()
		c.Series = append(c.Series, ln)
	}
	c.Empty = !drawn
	// Rótulo do valor atual na ponta: só quando não colide com o de outra série.
	for i := range c.Series {
		s := &c.Series[i]
		if len(s.Lines) == 0 {
			continue
		}
		s.EndLabel = true
		for j := range c.Series {
			if j != i && len(c.Series[j].Lines) > 0 && abs(c.Series[j].EndY-s.EndY) < 12 {
				s.EndLabel = false
			}
		}
	}
	if len(in.Series) == 1 && len(c.Series[0].Lines) > 0 {
		c.Now = c.Series[0].End
		switch {
		case in.NowWord != "":
			c.Now += " " + in.NowWord
		case in.Fmt != nil:
		case in.Metric != perfeval.MetricDisk:
			c.Now += " em uso"
		default:
			c.Now += " livre"
		}
	}
	var sub []string
	if c.LimitLabel != "" {
		sub = append(sub, c.LimitLabel)
	}
	if peak != nil {
		word := "pico"
		if in.Metric == perfeval.MetricDisk {
			word = "menor"
		}
		sub = append(sub, fmt.Sprintf("%s %s em %s", word, in.label(peak.V), in.when(peak.At)))
	}
	if n := len(c.Bands); n > 0 {
		word := "1 alerta no período"
		if n > 1 {
			word = fmt.Sprintf("%d alertas no período", n)
		}
		sub = append(sub, word+" (faixa vermelha)")
	}
	if in.MoreVolums > 0 {
		sub = append(sub, fmt.Sprintf("mais %d volume(s) na tabela", in.MoreVolums))
	}
	c.Sub = strings.Join(sub, " · ")
	c.Label = in.Title + ", de " + in.label(0) + " a " + in.label(in.top()) + ", de " + in.when(in.From) + " a " + in.when(in.To)
	c.Zones = buildZones(in)
	return c
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// buildZones divide o tempo em pcZones intervalos; em cada um, a linha-guia vai no ponto mais
// perto do meio, e a dica mostra o valor de cada série ali (e, numa série só, o pico do intervalo).
func buildZones(in chartIn) []chartZone {
	span := in.To.Sub(in.From)
	if span <= 0 {
		return nil
	}
	step := span / pcZones
	var out []chartZone
	for z := 0; z < pcZones; z++ {
		zFrom := in.From.Add(step * time.Duration(z))
		zTo := zFrom.Add(step)
		mid := zFrom.Add(step / 2)
		var rows []chartTipRow
		var dots []chartDot
		var at time.Time
		for i, s := range in.Series {
			var best *valPt
			worst := math.NaN()
			for k := range s.Pts {
				p := &s.Pts[k]
				if p.At.Before(zFrom) || !p.At.Before(zTo) {
					continue
				}
				if best == nil || absDur(p.At.Sub(mid)) < absDur(best.At.Sub(mid)) {
					best = p
				}
				if math.IsNaN(worst) {
					worst = p.Max
				} else {
					worst = worseOf(in.Metric, worst, p.Max)
				}
			}
			if best == nil {
				continue
			}
			if at.IsZero() {
				at = best.At
			}
			cls := "s" + strconv.Itoa(i+1)
			val := in.label(best.V)
			if in.Metric == perfeval.MetricDisk {
				val += " livre"
			}
			rows = append(rows, chartTipRow{Value: val, Name: s.Name, Class: cls})
			dots = append(dots, chartDot{X: pcX(best.At, in.From, in.To), Y: in.y(best.V), Class: cls})
			if len(in.Series) == 1 && worst != best.V {
				word := "pico do intervalo"
				if in.Metric == perfeval.MetricDisk {
					word = "menor do intervalo"
				}
				rows = append(rows, chartTipRow{Value: in.label(worst), Name: word, Class: "pico"})
			}
		}
		if len(rows) == 0 {
			continue
		}
		x1, x2 := pcX(zFrom, in.From, in.To), pcX(zTo, in.From, in.To)
		zn := chartZone{X: x1, W: max(1, x2-x1), LineX: pcX(at, in.From, in.To), Rows: rows, Dots: dots, When: in.when(at)}
		// A largura segue o texto (aproximação de 6,6 unidades por caractere em 11px).
		valW := 0
		nameW := len([]rune(zn.When))
		for _, r := range rows {
			valW = max(valW, len([]rune(r.Value)))
			nameW = max(nameW, len([]rune(r.Value))+len([]rune(r.Name))+2)
		}
		nameX := 16 + int(math.Ceil(float64(valW)*6.8)) + 8
		zn.BoxW = max(pcTipW, nameX+int(math.Ceil(float64(nameW-valW)*6.2))+12)
		zn.BoxH = 22 + pcTipRowH*len(rows)
		zn.BoxY = pcT
		// A dica vai do lado com espaço: à direita da guia, ou à esquerda perto do fim.
		if zn.LineX+10+zn.BoxW > pcR+40 {
			zn.BoxX = zn.LineX - 10 - zn.BoxW
		} else {
			zn.BoxX = zn.LineX + 10
		}
		zn.TextX = zn.BoxX + 10
		zn.WhenY = zn.BoxY + 15
		for i := range zn.Rows {
			r := &zn.Rows[i]
			r.Y = zn.BoxY + 31 + pcTipRowH*i
			r.KeyX1, r.KeyX2, r.ValueX = zn.TextX, zn.TextX+10, zn.TextX+16
			r.NameX = zn.TextX + nameX
		}
		out = append(out, zn)
	}
	return out
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// perfTableRow é uma linha da tabela que acompanha os gráficos.
type perfTableRow struct {
	When           string
	CPU, Mem, Disk string
}

// perfTable resume as séries em pcZones linhas (média da CPU e da memória, o menor disco livre).
func perfTable(in []chartIn) []perfTableRow {
	if len(in) == 0 {
		return nil
	}
	from, to := in[0].From, in[0].To
	step := to.Sub(from) / pcZones
	if step <= 0 {
		return nil
	}
	var out []perfTableRow
	for z := pcZones - 1; z >= 0; z-- { // mais recente primeiro
		zFrom := from.Add(step * time.Duration(z))
		zTo := zFrom.Add(step)
		row := perfTableRow{When: in[0].when(zFrom) + " – " + in[0].when(zTo)}
		has := false
		for _, c := range in {
			sum, n := 0.0, 0
			worst := math.NaN()
			for _, s := range c.Series {
				for _, p := range s.Pts {
					if p.At.Before(zFrom) || !p.At.Before(zTo) {
						continue
					}
					sum += p.V
					n++
					if math.IsNaN(worst) {
						worst = p.Max
					} else {
						worst = worseOf(c.Metric, worst, p.Max)
					}
				}
			}
			if n == 0 {
				continue
			}
			has = true
			switch c.Metric {
			case perfeval.MetricCPU:
				row.CPU = pctLabel(sum/float64(n)) + " (pico " + pctLabel(worst) + ")"
			case perfeval.MetricMem:
				row.Mem = pctLabel(sum/float64(n)) + " (pico " + pctLabel(worst) + ")"
			case perfeval.MetricDisk:
				row.Disk = pctLabel(worst) + " livre"
			}
		}
		if has {
			out = append(out, row)
		}
	}
	return out
}

// timeTicks: as marcas do eixo do tempo em horas cheias (24 h: a cada 6 h), dias (7 d) ou
// quinzenas (90 d), no fuso do painel.
func timeTicks(from, to time.Time, loc *time.Location) []chartTick {
	span := to.Sub(from)
	step, layout := 6*time.Hour, "15:04"
	switch {
	case span > 30*24*time.Hour:
		step, layout = 15*24*time.Hour, "02/01"
	case span > 48*time.Hour:
		step, layout = 24*time.Hour, "02/01"
	}
	f := from.In(loc)
	t := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc) // da meia-noite: 00, 06, 12 e 18 h
	for !t.After(f) {
		t = t.Add(step)
	}
	var out []chartTick
	for ; t.Before(to); t = t.Add(step) {
		x := pcX(t, from, to)
		anchor := "middle"
		switch {
		case x < pcL+20:
			anchor = "start"
		case x > pcR-20:
			anchor = "end"
		}
		out = append(out, chartTick{X: x, Label: t.Format(layout), Anchor: anchor})
	}
	return out
}
