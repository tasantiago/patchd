package protocol

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Desempenho (docs/monitoramento.md): o agente mede CPU, memória e disco a cada minuto e manda
// as amostras no check-in; o servidor guarda, abre e fecha os alertas.

// PerfSample é a medida de um minuto.
type PerfSample struct {
	At  time.Time `json:"at"`  // o fim do minuto medido (UTC)
	CPU float64   `json:"cpu"` // % de uso, média do minuto (0 a 100)
	Mem float64   `json:"mem"` // % de memória em uso (0 a 100)
	// Disks: % livre de cada volume fixo, pelo ponto de montagem ("C:", "/").
	Disks map[string]float64 `json:"disks,omitempty"`
}

// MonitorPolicy são os limites da frota, que o servidor manda no check-in. O agente os usa só
// para decidir quando adiantar o próximo check-in; quem abre o alerta é o servidor.
type MonitorPolicy struct {
	CPUPct      float64 `json:"cpu_pct"`       // alerta acima disso
	MemPct      float64 `json:"mem_pct"`       // alerta acima disso
	DiskFreePct float64 `json:"disk_free_pct"` // alerta abaixo disso
	Minutes     int     `json:"minutes"`       // por tantas amostras seguidas
}

// DefaultMonitorPolicy: os padrões (docs/monitoramento.md, seção 4).
var DefaultMonitorPolicy = MonitorPolicy{CPUPct: 90, MemPct: 90, DiskFreePct: 10, Minutes: 10}

// Limites do lote.
const (
	MaxPerfSamples = 1440 // 24 horas por minuto
	MaxPerfDisks   = 32
	MaxPerfMount   = 256
	MinPolicyMins  = 2
	MaxPolicyMins  = 120
)

// Check confere os limites.
func (p MonitorPolicy) Check() error {
	pct := func(v float64) bool { return v > 0 && v <= 100 && !math.IsNaN(v) }
	switch {
	case !pct(p.CPUPct) || !pct(p.MemPct) || !pct(p.DiskFreePct):
		return errors.New("limites de desempenho entre 0 e 100")
	case p.Minutes < MinPolicyMins || p.Minutes > MaxPolicyMins:
		return fmt.Errorf("minutos seguidos de %d a %d", MinPolicyMins, MaxPolicyMins)
	}
	return nil
}

// CheckPerf confere um lote: no máximo MaxPerfSamples, porcentagens entre 0 e 100, horas em
// ordem e sem repetição, nada no futuro além do desvio aceito.
func CheckPerf(samples []PerfSample, now time.Time) error {
	if len(samples) > MaxPerfSamples {
		return fmt.Errorf("lote de desempenho com %d amostras (máximo %d)", len(samples), MaxPerfSamples)
	}
	pct := func(v float64) bool { return v >= 0 && v <= 100 && !math.IsNaN(v) }
	var last time.Time
	for i, s := range samples {
		switch {
		case s.At.IsZero() || s.At.After(now.Add(24*time.Hour)):
			return fmt.Errorf("amostra %d: hora inválida", i)
		case !last.IsZero() && !s.At.After(last):
			return fmt.Errorf("amostra %d: fora de ordem", i)
		case !pct(s.CPU) || !pct(s.Mem):
			return fmt.Errorf("amostra %d: CPU ou memória fora de 0 a 100", i)
		case len(s.Disks) > MaxPerfDisks:
			return fmt.Errorf("amostra %d: volumes demais", i)
		}
		for m, v := range s.Disks {
			if m == "" || len(m) > MaxPerfMount || !pct(v) {
				return fmt.Errorf("amostra %d: volume inválido", i)
			}
		}
		last = s.At
	}
	return nil
}
