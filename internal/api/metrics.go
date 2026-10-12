package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/metrics"
)

// Métricas da API (Aula 10.2): contadores incrementados onde as coisas acontecem. A rota é
// o padrão do ServeMux ("POST /api/v1/agent/checkin"), nunca o caminho cru: o número de
// séries fica limitado ao número de rotas, por mais IDs de máquina que passem.
type Metrics struct {
	requests *metrics.CounterVec
	seconds  *metrics.CounterVec
	checkins *metrics.CounterVec
	jobs     *metrics.CounterVec
	logins   *metrics.CounterVec
}

// NewMetrics registra as métricas da API no registro.
func NewMetrics(r *metrics.Registry) *Metrics {
	return &Metrics{
		requests: r.NewCounter("patchd_http_requests_total", "Requisições atendidas, por rota e código.", "rota", "codigo"),
		seconds:  r.NewCounter("patchd_http_request_seconds_total", "Tempo gasto nas requisições, por rota (divida pelo número de requisições para a média).", "rota"),
		checkins: r.NewCounter("patchd_checkins_total", "Check-ins dos agentes, por canal (https ou http).", "canal"),
		jobs:     r.NewCounter("patchd_jobs_encerrados_total", "Jobs encerrados com o resultado do agente, por tipo e estado.", "tipo", "estado"),
		logins:   r.NewCounter("patchd_logins_total", "Logins no painel, por método (senha ou rhbk) e resultado.", "metodo", "resultado"),
	}
}

// WithMetrics liga os contadores da API.
func WithMetrics(m *Metrics) Option {
	return func(a *api) { a.metrics = m }
}

// observe registra a requisição já atendida (chamado pelo log de acesso).
func (a *api) observe(r *http.Request, status int, d time.Duration) {
	if a.metrics == nil {
		return
	}
	route := r.Pattern
	if route == "" {
		route = "(nenhuma)"
	}
	a.metrics.requests.Inc(route, strconv.Itoa(status))
	a.metrics.seconds.Add(d.Seconds(), route)
}

// count soma 1 num contador da API, se as métricas estiverem ligadas.
func (a *api) count(pick func(*Metrics) *metrics.CounterVec, values ...string) {
	if a.metrics != nil {
		pick(a.metrics).Inc(values...)
	}
}

func checkinsOf(m *Metrics) *metrics.CounterVec { return m.checkins }
func jobsOf(m *Metrics) *metrics.CounterVec     { return m.jobs }
func loginsOf(m *Metrics) *metrics.CounterVec   { return m.logins }
