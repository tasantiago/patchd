// Package metrics expõe números do patchd no formato de texto do Prometheus (Aula 10.2),
// só com a biblioteca padrão: contadores com rótulos, incrementados onde as coisas
// acontecem, e medidores calculados na hora da coleta.
//
// Formato: https://prometheus.io/docs/instrumenting/exposition_formats/ (texto 0.0.4).
package metrics

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Registry guarda as famílias de métricas, na ordem em que foram registradas.
type Registry struct {
	mu       sync.Mutex
	families []family
	names    map[string]bool
}

type family interface {
	write(w *bufio.Writer)
}

// NewRegistry cria um registro vazio.
func NewRegistry() *Registry { return &Registry{names: map[string]bool{}} }

func (r *Registry) add(name string, labels []string, f family) {
	if !namePattern.MatchString(name) {
		panic("metrics: nome inválido " + name)
	}
	for _, l := range labels {
		if !namePattern.MatchString(l) || strings.HasPrefix(l, "__") {
			panic("metrics: rótulo inválido " + l)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[name] {
		panic("metrics: nome repetido " + name)
	}
	r.names[name] = true
	r.families = append(r.families, f)
}

// CounterVec é um contador com rótulos: só sobe (reinicia em zero com o processo).
type CounterVec struct {
	name, help string
	labels     []string
	mu         sync.Mutex
	values     map[string]*sample
}

type sample struct {
	labels []string
	value  float64
}

// NewCounter registra um contador. O nome deve terminar em _total (convenção).
func (r *Registry) NewCounter(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, labels: labels, values: map[string]*sample{}}
	r.add(name, labels, c)
	return c
}

// Inc soma 1 na série dos valores de rótulo dados (na ordem dos rótulos).
func (c *CounterVec) Inc(values ...string) { c.Add(1, values...) }

// Add soma v (v >= 0) na série.
func (c *CounterVec) Add(v float64, values ...string) {
	if c == nil || v < 0 || math.IsNaN(v) {
		return
	}
	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("metrics: %s espera %d rótulos, recebeu %d", c.name, len(c.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.values[key]
	if !ok {
		s = &sample{labels: append([]string(nil), values...)}
		c.values[key] = s
	}
	s.value += v
}

// Value devolve o valor atual da série (para os testes).
func (c *CounterVec) Value(values ...string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.values[strings.Join(values, "\xff")]; ok {
		return s.value
	}
	return 0
}

func (c *CounterVec) write(w *bufio.Writer) {
	c.mu.Lock()
	list := make([]sample, 0, len(c.values))
	for _, s := range c.values {
		list = append(list, *s)
	}
	c.mu.Unlock()
	writeFamily(w, c.name, c.help, "counter", c.labels, list)
}

// Sample é uma série de um medidor: os valores dos rótulos (na ordem) e o número.
type Sample struct {
	Labels []string
	Value  float64
}

// GaugeFunc é um medidor calculado na hora da coleta.
type GaugeFunc struct {
	name, help string
	labels     []string
	fn         func() []Sample
}

// NewGaugeFunc registra um medidor; fn devolve as séries a cada coleta (nil: nenhuma).
func (r *Registry) NewGaugeFunc(name, help string, labels []string, fn func() []Sample) {
	r.add(name, labels, &GaugeFunc{name: name, help: help, labels: labels, fn: fn})
}

func (g *GaugeFunc) write(w *bufio.Writer) {
	var list []sample
	for _, s := range g.fn() {
		if len(s.Labels) != len(g.labels) {
			continue // série malformada: fora, em vez de derrubar a coleta
		}
		list = append(list, sample{labels: s.Labels, value: s.Value})
	}
	writeFamily(w, g.name, g.help, "gauge", g.labels, list)
}

func writeFamily(w *bufio.Writer, name, help, typ string, labels []string, list []sample) {
	sort.Slice(list, func(i, j int) bool {
		return strings.Join(list[i].labels, "\xff") < strings.Join(list[j].labels, "\xff")
	})
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
	for _, s := range list {
		w.WriteString(name)
		if len(labels) > 0 {
			w.WriteByte('{')
			for i, l := range labels {
				if i > 0 {
					w.WriteByte(',')
				}
				fmt.Fprintf(w, `%s="%s"`, l, escapeLabel(s.labels[i]))
			}
			w.WriteByte('}')
		}
		w.WriteByte(' ')
		w.WriteString(formatFloat(s.value))
		w.WriteByte('\n')
	}
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64) // inteiros (contagens, horários Unix) por extenso
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// WriteText escreve todas as famílias no formato de texto.
func (r *Registry) WriteText(out io.Writer) error {
	r.mu.Lock()
	fams := append([]family(nil), r.families...)
	r.mu.Unlock()
	w := bufio.NewWriter(out)
	for _, f := range fams {
		f.write(w)
	}
	return w.Flush()
}

// Handler serve GET /metrics com o token (Bearer) exigido. A comparação é em tempo
// constante, sobre o SHA-256 dos dois lados (o tamanho do token não vaza).
func (r *Registry) Handler(token string) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/metrics" {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got))
		if !ok || token == "" || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="patchd-metrics"`)
			http.Error(w, "token das métricas ausente ou errado", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = r.WriteText(w)
	})
}
