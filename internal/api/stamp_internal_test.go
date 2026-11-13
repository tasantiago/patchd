package api

import (
	"testing"
	"time"
)

// A hora das telas: no fuso do painel quando há um; sem fuso, em UTC e dito; e uma data
// que não existe sai como traço, nunca como "01/01/0001".
func TestHoraNoFusoDoPainel(t *testing.T) {
	quando := time.Date(2026, 11, 13, 14, 30, 0, 0, time.UTC)
	for fuso, quer := range map[string]string{
		"":                    "13/11/2026 14:30 UTC",
		"America/Porto_Velho": "13/11/2026 10:30",
		"Fuso/Inexistente":    "13/11/2026 14:30 UTC",
	} {
		a := &api{timezone: fuso}
		if got := a.stamp(quando); got != quer {
			t.Errorf("fuso %q: %q, esperava %q", fuso, got, quer)
		}
		if got := a.stamp(time.Time{}); got != "—" {
			t.Errorf("fuso %q, data vazia: %q", fuso, got)
		}
		if got := a.expiry(time.Time{}); got != "sem vencimento" {
			t.Errorf("fuso %q, token sem validade: %q", fuso, got)
		}
	}
	for n, quer := range map[any]string{0: "v-zero", int64(0): "v-zero", "0": "v-zero", 3: "v-vermelho", "0%": "v-vermelho", "12": "v-vermelho"} {
		if got := kpiColor("v-vermelho", n); got != quer {
			t.Errorf("cor de %v: %q, esperava %q", n, got, quer)
		}
	}
}
