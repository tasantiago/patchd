package jobs

import (
	"strings"
	"testing"
	"time"
)

// Aula 17.6: o reinício na hora escolhida pelo usuário.
func TestRebootNaHoraEscolhida(t *testing.T) {
	agora := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	raw, err := RebootAtParamsJSON(agora.Add(4 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"at":"2026-10-17T18:00:00Z"`) || !strings.Contains(string(raw), `"delay_minutes":2`) {
		t.Fatalf("parâmetros: %s", raw)
	}
	p, err := ParseRebootParams(raw)
	if err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		now  time.Time
		quer time.Duration
	}{
		{agora, 4 * time.Hour}, // entregue na hora: até as 18:00
		{agora.Add(3*time.Hour + 30*time.Minute), 30 * time.Minute}, // entregue mais tarde: o que falta
		{agora.Add(5 * time.Hour), MinRebootDelay},                  // a hora passou (máquina desligada): o aviso mínimo
		{agora.Add(-10 * time.Hour), MaxScheduledReboot},            // relógio da máquina atrasado: nunca mais que 12 h
	}
	for _, c := range casos {
		if got := p.DelayAt(c.now); got != c.quer {
			t.Errorf("às %s: espera %v, quer %v", c.now.Format("15:04"), got, c.quer)
		}
	}
	// Sem a hora (o admin, ou "agora"): o aviso de sempre.
	old, _ := ParseRebootParams([]byte(`{"delay_minutes":15}`))
	if old.DelayAt(agora) != 15*time.Minute {
		t.Error("sem at, vale o delay_minutes")
	}
}
