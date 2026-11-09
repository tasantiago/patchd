package main

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/api"
)

// A porta só de agentes (docs/rele.md): o relé na nuvem repassa para ela, pelo túnel, as
// conexões dos notebooks fora da rede. Ela atende só as rotas do agente (com a credencial da
// máquina, como sempre) e o /healthz; o painel, a API de leitura e o registro de máquina nova
// ficam de fora: uma porta alcançável pela internet não mostra a tela de login a ninguém. As
// requisições levam a marca "pelo relé", e o check-in guarda por onde a máquina falou.

// refusalLog: a internet varre portas o tempo todo; o log das recusas sai no máximo uma vez
// por minuto, com quantas foram.
type refusalLog struct {
	mu      sync.Mutex
	last    time.Time
	skipped int
}

func (l *refusalLog) allow(now time.Time) (ok bool, skipped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.last) < time.Minute {
		l.skipped++
		return false, 0
	}
	l.last, skipped, l.skipped = now, l.skipped, 0
	return true, skipped
}

func agentOnlyHandler(full http.Handler, logger *slog.Logger) http.Handler {
	var refusals refusalLog
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/v1/agent/") || p == "/healthz" {
			full.ServeHTTP(w, r.WithContext(api.ViaRelay(r.Context())))
			return
		}
		if ok, skipped := refusals.allow(time.Now()); ok {
			logger.Warn("pedido recusado na porta só de agentes (relé): painel, API e registro não abrem por ela",
				"method", r.Method, "path", p, "remote", r.RemoteAddr, "recusas_no_ultimo_minuto", skipped)
		}
		http.Error(w, "esta porta é só dos agentes do patchd", http.StatusForbidden)
	})
}
