package api

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/standardization"
)

// O check-in não reavalia a padronização da máquina avaliada há menos de 10 minutos (pelo
// próprio check-in ou pelo app do usuário); gravar uma política zera isso.
func TestPadronizacaoNoCheckinComIntervalo(t *testing.T) {
	agora := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	a := &api{now: func() time.Time { return agora }, stdRecent: map[string]time.Time{},
		stdPolicies: &standardization.PolicyCache{TTL: time.Minute}}
	if !a.stdTouch("pc", stdCheckinGap) {
		t.Fatal("primeira avaliação")
	}
	agora = agora.Add(9 * time.Minute)
	if a.stdTouch("pc", stdCheckinGap) {
		t.Error("reavaliou em 9 minutos")
	}
	if !a.stdTouch("outro", stdCheckinGap) {
		t.Error("outra máquina")
	}
	// O pedido do app do usuário (intervalo 0) sempre avalia, e conta para o check-in.
	agora = agora.Add(2 * time.Minute) // 11 min depois da primeira
	if !a.stdTouch("pc", 0) {
		t.Error("o pedido do usuário sempre avalia")
	}
	agora = agora.Add(5 * time.Minute)
	if a.stdTouch("pc", stdCheckinGap) {
		t.Error("o check-in devia contar a avaliação do usuário de 5 minutos atrás")
	}
	a.stdReset()
	if !a.stdTouch("pc", stdCheckinGap) {
		t.Error("depois de gravar uma política, o check-in avalia de novo")
	}
	// Relógio que voltou: avalia.
	agora = agora.Add(-time.Hour)
	if !a.stdTouch("pc", stdCheckinGap) {
		t.Error("relógio voltou")
	}
}
