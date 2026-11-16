package userapp

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O intervalo varia até 20% para os dois lados; o relato igual é reconhecido.
func TestPadronizacaoEspalhadaERelatoIgual(t *testing.T) {
	for range 200 {
		if d := stdJitter(5 * time.Minute); d < 4*time.Minute || d > 6*time.Minute {
			t.Fatalf("intervalo %v fora de 4 a 6 minutos", d)
		}
	}
	a := &protocol.StandardizationResult{Revision: "r", Items: []protocol.StandardShortcutResult{{Name: "Portal", State: "em_dia"}}}
	b := &protocol.StandardizationResult{Revision: "r", Items: []protocol.StandardShortcutResult{{Name: "Portal", State: "em_dia"}}}
	if !sameResult(a, b) || sameResult(a, nil) {
		t.Error("relato igual")
	}
	b.Items[0].State = "aplicado"
	if sameResult(a, b) {
		t.Error("relato diferente passou por igual")
	}
}
