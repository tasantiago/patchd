package api

import (
	"net/http"
	"strconv"
	"sync"
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
	// Aula 20.5: requisições de máquinas recusadas pelo limite, por classe.
	agentLimits *metrics.CounterVec
	// PatchD Shield (docs/shield.md): as passadas do avaliador e as máquinas por faixa (a da
	// última passada deste servidor). Sem rótulo de máquina ou de pessoa.
	shieldRuns *metrics.CounterVec
	shieldMu   sync.Mutex
	shieldBand map[string]int
	shieldSecs float64
}

// NewMetrics registra as métricas da API no registro.
func NewMetrics(r *metrics.Registry) *Metrics {
	m := &Metrics{
		requests: r.NewCounter("patchd_http_requests_total", "Requisições atendidas, por rota e código.", "rota", "codigo"),
		seconds:  r.NewCounter("patchd_http_request_seconds_total", "Tempo gasto nas requisições, por rota (divida pelo número de requisições para a média).", "rota"),
		checkins: r.NewCounter("patchd_checkins_total", "Check-ins dos agentes, por canal (https ou http).", "canal"),
		jobs:     r.NewCounter("patchd_jobs_encerrados_total", "Jobs encerrados com o resultado do agente, por tipo e estado.", "tipo", "estado"),
		logins:   r.NewCounter("patchd_logins_total", "Logins no painel, por método (senha ou rhbk) e resultado.", "metodo", "resultado"),
		agentLimits: r.NewCounter("patchd_agente_limitado_total",
			"Requisições de máquinas recusadas pelo limite por máquina (429), por classe (relatorio ou geral).", "classe"),
		shieldRuns: r.NewCounter("patchd_shield_avaliacoes_total", "Passadas do avaliador do Shield neste servidor, por resultado (ok ou erro).", "resultado"),
	}
	r.NewGaugeFunc("patchd_shield_maquinas", "Máquinas do Shield por faixa, na última passada do avaliador neste servidor.", []string{"faixa"}, func() []metrics.Sample {
		m.shieldMu.Lock()
		defer m.shieldMu.Unlock()
		out := make([]metrics.Sample, 0, len(m.shieldBand))
		for _, b := range []string{"protegida", "quase", "atencao", "risco", "sem_dado"} {
			if m.shieldBand != nil {
				out = append(out, metrics.Sample{Labels: []string{b}, Value: float64(m.shieldBand[b])})
			}
		}
		return out
	})
	r.NewGaugeFunc("patchd_shield_avaliacao_segundos", "Duração da última passada do avaliador do Shield neste servidor.", nil, func() []metrics.Sample {
		m.shieldMu.Lock()
		defer m.shieldMu.Unlock()
		return []metrics.Sample{{Value: m.shieldSecs}}
	})
	return m
}

// shieldObserve guarda a passada do avaliador do Shield (counts nil: a passada falhou ou o
// módulo está desligado).
func (m *Metrics) shieldObserve(counts map[string]int, d time.Duration, failed bool) {
	if m == nil {
		return
	}
	res := "ok"
	if failed {
		res = "erro"
	}
	m.shieldRuns.Inc(res)
	m.shieldMu.Lock()
	defer m.shieldMu.Unlock()
	if !failed {
		m.shieldBand = counts
	}
	m.shieldSecs = d.Seconds()
}

// WithMetrics liga os contadores da API.
func WithMetrics(m *Metrics) Option {
	return func(a *api) { a.metrics = m }
}

// observe registra a requisição já atendida (chamado pelo log de acesso).
func (a *api) observe(r *http.Request, status int, d time.Duration) {
	route := r.Pattern
	if route == "" {
		route = "(nenhuma)"
	}
	a.health.Observe(route, status, d, transferRoute(route)) // a aba Saúde (docs/saude-servidor.md); nil: desligada
	if a.metrics == nil {
		return
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
