package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/srvhealth"
)

type bancoSaude struct{ horas []srvhealth.Hour }

func (bancoSaude) PoolStat() srvhealth.PoolStat {
	return srvhealth.PoolStat{Max: 8, Total: 3, InUse: 1, Acquires: 1000, Waits: 2}
}
func (bancoSaude) DBStats(context.Context) (srvhealth.DBStats, error) {
	return srvhealth.DBStats{Bytes: 3 << 30, Connections: 9, MaxConnections: 100, CacheHitPct: 99.8, BlocksRead: 1e6,
		Tables: []srvhealth.TableSize{{Name: "inventory_history", Bytes: 2 << 30}}}, nil
}
func (bancoSaude) SaveHealthHour(context.Context, srvhealth.Hour) error { return nil }
func (b bancoSaude) HealthHours(context.Context, time.Time) ([]srvhealth.Hour, error) {
	return b.horas, nil
}
func (bancoSaude) PruneHealth(context.Context, time.Time) (int64, error) { return 0, nil }

// TestSaudeDoServidor (docs/saude-servidor.md): a aba Saúde mostra o veredito, o que fazer,
// os gráficos e onde o servidor gasta tempo; só para o admin; sem o monitor, não existe.
func TestSaudeDoServidor(t *testing.T) {
	const aba = "/painel/configuracao?aba=saude"
	h, _, _ := ambientePaginas(t)
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); strings.Contains(b, "aba=saude") {
		t.Fatal("sem o monitor, a aba Saúde não aparece")
	}

	now := time.Now()
	horas := []srvhealth.Hour{{Hour: now.Add(-72 * time.Hour).Truncate(time.Hour), Samples: 60, CPUAvg: 20, CPUMax: 40, HostCPUAvg: 30, HostCPUMax: 50,
		MemAvg: 40, MemMax: 45, HostMemMax: 60, Requests: 600, LatAvgMs: 20, LatP95Max: 90, PoolInUseMax: 2, PoolMax: 8, DiskFreeMin: 70, DBBytes: 2 << 30}}
	m := srvhealth.New(bancoSaude{horas: horas}, nil, nil)
	for i := 0; i < 24*60; i++ {
		p := srvhealth.Point{At: now.Add(time.Duration(i-24*60) * time.Minute), CPUPct: 20, HostCPUPct: 30, MemPct: 40, MemBytes: 400 << 20,
			HostMemPct: 50, Requests: 10, Timed: 10, LatAvgMs: 20, LatP95Ms: 60, PoolInUse: 2, PoolMax: 8, PoolAcquires: 20, DiskFreePct: srvhealth.Unknown}
		if i%10 == 0 {
			p.CPUPct = 96
		}
		m.AddPoint(p)
	}
	h, _, _ = ambientePaginas(t, api.WithServerHealth(m))
	if r := pagina(h, "GET", aba, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Fatalf("leitura vê a saúde: %d", r.Code)
	}
	r := pagina(h, "GET", aba, sessaoAdmin, nil)
	b := r.Body.String()
	for _, quer := range []string{
		`href="/painel/configuracao?aba=saude" class="ativa"`,
		"Aumente agora: cpu", "A CPU do patchd está no limite", "O que fazer:",
		"Tempo de resposta (p95)", "Pool do banco", "<svg class=\"grafico\"",
		"Onde o servidor gasta tempo", "GET /painel/configuracao", // a própria requisição da tela anterior
		"inventory_history", "3,0 GB", "9 de 100",
		"O banco cresce cerca de",
	} {
		if !strings.Contains(b, quer) {
			t.Errorf("a aba não tem %q", quer)
		}
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Error("a saúde não pode ficar em cache")
	}
	b = pagina(h, "GET", aba+"&periodo=7d", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Tamanho do banco") || !strings.Contains(b, `periodo=7d" class="ativa"`) {
		t.Errorf("7 dias sem o tamanho do banco:\n%.2000s", b)
	}
	// Sem banco: só as 24 horas.
	h, _, _ = ambientePaginas(t, api.WithServerHealth(srvhealth.New(nil, nil, nil)))
	b = pagina(h, "GET", aba+"&periodo=90d", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Medindo") || strings.Contains(b, "periodo=90d") || !strings.Contains(b, "sem banco") {
		t.Errorf("sem banco:\n%.3000s", b)
	}
}
