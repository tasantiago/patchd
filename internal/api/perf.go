package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Desempenho no check-in (docs/monitoramento.md): as amostras do agente são gravadas e passam
// pela regra dos alertas; a resposta leva os limites da frota.

// monitorPolicy: os limites que valem agora (Configuração › Parâmetros › Monitoramento).
func (a *api) monitorPolicy() protocol.MonitorPolicy {
	p := protocol.DefaultMonitorPolicy
	if a.params == nil {
		return p
	}
	num := func(key string, dst *float64) {
		if v, err := strconv.ParseFloat(a.params.Value(key), 64); err == nil {
			*dst = v
		}
	}
	num("PATCHD_MONITOR_CPU_PCT", &p.CPUPct)
	num("PATCHD_MONITOR_MEM_PCT", &p.MemPct)
	num("PATCHD_MONITOR_DISK_FREE_PCT", &p.DiskFreePct)
	if n, err := strconv.Atoi(a.params.Value("PATCHD_MONITOR_MINUTES")); err == nil {
		p.Minutes = n
	}
	if p.Check() != nil {
		return protocol.DefaultMonitorPolicy
	}
	return p
}

// checkinMinutes: o intervalo de check-in de Configuração › Parâmetros › Agentes; 0 (o
// padrão, ou valor fora dos limites): cada agente usa o seu.
func (a *api) checkinMinutes() int {
	if a.params == nil {
		return 0
	}
	n, err := strconv.Atoi(a.params.Value("PATCHD_CHECKIN_MINUTES"))
	if err != nil || !protocol.ValidCheckinMinutes(n) {
		return 0
	}
	return n
}

// checkinWait: a espera máxima por um check-in, em palavras ("cerca de 15 min"). Sem o
// parâmetro, o padrão dos agentes (1 hora).
func (a *api) checkinWait() string {
	switch n := a.checkinMinutes(); {
	case n == 0 || n == 60:
		return "cerca de 1 h"
	case n%60 == 0:
		return fmt.Sprintf("cerca de %d h", n/60)
	default:
		return fmt.Sprintf("cerca de %d min", n)
	}
}

// savePerf grava as amostras. Como o estado da máquina, é dado de conveniência: um lote fora
// do formato é descartado com aviso e uma falha ao gravar não derruba o check-in (o agente não
// reenvia: ele tira as amostras da fila quando o check-in passa).
func (a *api) savePerf(r *http.Request, id string, samples []protocol.PerfSample, policy protocol.MonitorPolicy) {
	if len(samples) == 0 {
		return
	}
	ctx := r.Context()
	if err := protocol.CheckPerf(samples, a.now()); err != nil {
		a.logger.Warn("amostras de desempenho descartadas", "machine_id", id, "reason", err.Error(), "request_id", RequestID(ctx))
		return
	}
	changes, err := a.store.SavePerf(ctx, id, samples, policy, a.now())
	if err != nil {
		a.logger.Error("gravar desempenho", "machine_id", id, "error", err, "request_id", RequestID(ctx))
		return
	}
	for _, c := range changes {
		what := "alerta de desempenho fechado"
		if c.Opened {
			what = "alerta de desempenho aberto"
		}
		a.logger.Warn(what, "machine_id", id, "medida", c.Key.Metric, "volume", c.Key.Target, "valor", c.Value, "pico", c.Peak,
			"request_id", RequestID(ctx))
	}
}
