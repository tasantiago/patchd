package api

import (
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/srvhealth"
)

// Os arquivos temporários do PostgreSQL são um total acumulado: a tela diz desde quando, o
// ritmo por dia e que não é espaço ocupado.
func TestTemporariosSaoAcumulados(t *testing.T) {
	agora := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	data := func(t time.Time) string { return t.Format("02/01/2006") }
	got := tempLabel(srvhealth.DBStats{TempBytes: 40 << 30, StatsReset: agora.Add(-10 * 24 * time.Hour)}, agora, data)
	for _, quer := range []string{"40,0 GB escritos desde 06/11/2026", "cerca de 4,0 GB por dia", "não espaço ocupado"} {
		if !strings.Contains(got, quer) {
			t.Errorf("falta %q em %q", quer, got)
		}
	}
	got = tempLabel(srvhealth.DBStats{TempBytes: 1 << 20}, agora, data)
	if !strings.Contains(got, "desde a última limpeza das estatísticas") || strings.Contains(got, "por dia") {
		t.Errorf("sem a data da limpeza: %q", got)
	}
}
