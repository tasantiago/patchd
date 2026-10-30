// Package perfeval é a regra dos alertas de desempenho (docs/monitoramento.md, seção 4): N
// amostras seguidas além do limite abrem; N seguidas dentro fecham. A mesma regra roda no
// servidor, que abre e fecha os alertas, e no agente, que só adianta o check-in quando o
// estado muda.
package perfeval

import (
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Medidas.
const (
	MetricCPU  = "cpu"
	MetricMem  = "mem"
	MetricDisk = "disk"
)

// MaxGap: um buraco maior que isso entre duas amostras (máquina desligada, agente parado)
// zera a contagem; um alerta aberto continua aberto.
const MaxGap = 5 * time.Minute

// Key é o que um alerta mede: a medida e, no disco, o volume.
type Key struct {
	Metric string `json:"metric"`
	Target string `json:"target,omitempty"`
}

// Track é o estado de uma medida.
type Track struct {
	Open bool      `json:"open"`           // além do limite (alerta aberto)
	Run  int       `json:"run"`            // amostras seguidas do lado contrário ao estado
	Peak float64   `json:"peak,omitempty"` // o pior valor desde que abriu
	Last float64   `json:"last"`
	At   time.Time `json:"at"` // a última amostra vista
}

// State é o estado de todas as medidas de uma máquina.
type State map[string]*Track

func (k Key) id() string { return k.Metric + "|" + k.Target }

// KeyOf desfaz o id.
func KeyOf(id string) Key {
	for i := 0; i < len(id); i++ {
		if id[i] == '|' {
			return Key{Metric: id[:i], Target: id[i+1:]}
		}
	}
	return Key{Metric: id}
}

// Change é uma mudança de estado: abriu (Opened) ou fechou.
type Change struct {
	Key    Key
	Opened bool
	At     time.Time
	Value  float64 // o valor da amostra que mudou o estado
	Peak   float64 // o pior valor enquanto esteve aberto
}

// Breach diz se o valor está além do limite.
func Breach(p protocol.MonitorPolicy, k Key, v float64) bool {
	switch k.Metric {
	case MetricCPU:
		return v > p.CPUPct
	case MetricMem:
		return v > p.MemPct
	case MetricDisk:
		return v < p.DiskFreePct
	}
	return false
}

// worse diz se a é pior que b na medida (disco: menos livre é pior).
func worse(metric string, a, b float64) bool {
	if metric == MetricDisk {
		return a < b
	}
	return a > b
}

// Step passa uma amostra pelo estado e devolve as mudanças, em ordem estável.
func (st State) Step(p protocol.MonitorPolicy, s protocol.PerfSample) []Change {
	vals := map[Key]float64{{Metric: MetricCPU}: s.CPU, {Metric: MetricMem}: s.Mem}
	for m, v := range s.Disks {
		vals[Key{Metric: MetricDisk, Target: m}] = v
	}
	keys := make([]Key, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].id() < keys[j].id() })
	var out []Change
	for _, k := range keys {
		v := vals[k]
		t := st[k.id()]
		if t == nil {
			t = &Track{}
			st[k.id()] = t
		}
		if !t.At.IsZero() && s.At.Sub(t.At) > MaxGap {
			t.Run = 0
		}
		t.At, t.Last = s.At, v
		if t.Open && worse(k.Metric, v, t.Peak) {
			t.Peak = v
		}
		if Breach(p, k, v) != t.Open {
			t.Run++
		} else {
			t.Run = 0
		}
		if t.Run < p.Minutes {
			continue
		}
		t.Run = 0
		t.Open = !t.Open
		c := Change{Key: k, Opened: t.Open, At: s.At, Value: v, Peak: t.Peak}
		if t.Open {
			t.Peak, c.Peak = v, v
		} else {
			t.Peak = 0
		}
		out = append(out, c)
	}
	// Volume que sumiu (pendrive não entra, mas um disco pode sair): esquece, sem fechar à
	// força; o servidor fecha o alerta dele quando o volume voltar abaixo do limite.
	return out
}
