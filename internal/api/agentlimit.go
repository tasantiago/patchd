package api

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/metrics"
)

// Limite de requisições por máquina (Aula 20.5, S6). A credencial de uma máquina
// comprometida (T3) é válida, e até aqui ela podia mandar quantos inventários quisesse:
// cada um com hash novo vira uma linha nova no banco, de até 32 MB descomprimido. O limite é
// por máquina (não por IP: atrás de NAT, muitas máquinas dividem um endereço) e em duas
// classes, com folga larga para o agente normal, que faz um check-in por hora:
//
//   - relatório (inventário e busca): 10 de uma vez, depois 1 a cada 3 minutos (20 por hora);
//   - o resto (check-in, jobs, downloads, autoatendimento): 60 de uma vez, depois 1 a cada
//     5 segundos (720 por hora).
//
// Acima disso, 429 com Retry-After. O agente trata o 429 como erro passageiro e tenta de novo
// mais tarde (Aula 4.x). O limite é da memória do processo: com dois servidores, cada um
// conta o seu, o que ainda limita.

type limitClass struct {
	name  string
	burst float64       // requisições de uma vez
	every time.Duration // uma a mais a cada
}

var (
	limitReports = limitClass{"relatorio", 10, 3 * time.Minute}
	limitGeneral = limitClass{"geral", 60, 5 * time.Second}
)

// agentLimiter: um balde de fichas por máquina e classe.
type agentLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	warned  map[string]time.Time // último aviso no log por máquina (um a cada 10 min)
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// allow gasta uma ficha; sem ficha, diz quanto falta para a próxima.
func (l *agentLimiter) allow(machine string, c limitClass, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets, l.warned = map[string]*bucket{}, map[string]time.Time{}
	}
	l.sweep(now)
	key := c.name + "|" + machine
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: c.burst, at: now}
		l.buckets[key] = b
	}
	b.refill(c, now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(c.every))
}

func (b *bucket) refill(c limitClass, now time.Time) {
	if d := now.Sub(b.at); d > 0 {
		b.tokens = math.Min(c.burst, b.tokens+float64(d)/float64(c.every))
		b.at = now
	}
}

// sweep esquece, a cada 10 minutos, os baldes que já encheram de novo (máquinas quietas).
func (l *agentLimiter) sweep(now time.Time) {
	if now.Sub(l.swept) < 10*time.Minute {
		return
	}
	l.swept = now
	for k, b := range l.buckets {
		c := limitGeneral
		if strings.HasPrefix(k, limitReports.name+"|") {
			c = limitReports
		}
		if b.refill(c, now); b.tokens >= c.burst {
			delete(l.buckets, k)
		}
	}
	for k, t := range l.warned {
		if now.Sub(t) >= 10*time.Minute {
			delete(l.warned, k)
		}
	}
}

// warn diz se é hora de registrar o excesso desta máquina no log (um aviso a cada 10
// minutos: uma máquina insistindo não enche o log).
func (l *agentLimiter) warn(machine string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.warned[machine]; ok && now.Sub(t) < 10*time.Minute {
		return false
	}
	l.warned[machine] = now
	return true
}

// limitFor: a classe da requisição.
func limitFor(r *http.Request) limitClass {
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/agent/inventory" || r.URL.Path == "/api/v1/agent/scan") {
		return limitReports
	}
	return limitGeneral
}

// checkAgentLimit responde 429 e devolve false quando a máquina passou do limite.
func (a *api) checkAgentLimit(w http.ResponseWriter, r *http.Request, machine string) bool {
	c, now := limitFor(r), a.now()
	ok, wait := a.agentLimits.allow(machine, c, now)
	if ok {
		return true
	}
	secs := int(math.Ceil(wait.Seconds()))
	a.count(agentLimitsOf, c.name)
	if a.agentLimits.warn(machine, now) {
		a.logger.Warn("máquina acima do limite de requisições", "machine_id", machine, "classe", c.name,
			"proxima_em_s", secs, "path", r.URL.Path, "request_id", RequestID(r.Context()), "remote", r.RemoteAddr)
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited",
		"requisições demais desta máquina; tente de novo em "+strconv.Itoa(secs)+" s")
	return false
}

func agentLimitsOf(m *Metrics) *metrics.CounterVec { return m.agentLimits }
