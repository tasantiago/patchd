package compliance

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	recente := agora.Add(-2 * time.Hour)
	sim, nao := true, false
	casos := []struct {
		nome   string
		f      Facts
		estado State
		motivo string
	}{
		{"em dia", Facts{LastSeen: recente, RebootPending: &nao}, StateOK, ""},
		{"busca não sabe do reinício", Facts{LastSeen: recente}, StateOK, ""},
		{"faltando", Facts{LastSeen: recente, Pending: 3, PendingUnit: "CVEs", Exploited: 1}, StateMissing, "3 CVEs com correção pendente (1 com exploração conhecida)"},
		{"faltando e reinício: os dois motivos", Facts{LastSeen: recente, Pending: 2, PendingUnit: "pacotes", RebootPending: &sim}, StateMissing, "reinício pendente"},
		{"reinício pela busca", Facts{LastSeen: recente, RebootPending: &sim}, StateReboot, "informado pela busca"},
		{"reinício pelo kernel", Facts{LastSeen: recente, NewerKernel: true}, StateReboot, "kernel mais novo"},
		{"macOS sem suporte", Facts{LastSeen: recente, SupportEnded: true}, StateMissing, "não recebe mais correções"},
		{"sem inventário", Facts{LastSeen: recente, NoInventory: true}, StateUnknown, "ainda não enviou inventário"},
		{"sem catálogo", Facts{LastSeen: recente, Unevaluated: "Fedora 42 fora do catálogo"}, StateUnknown, "sem avaliação: Fedora 42"},
		{"sem catálogo com reinício", Facts{LastSeen: recente, Unevaluated: "x", RebootPending: &sim}, StateUnknown, "reinício pendente"},
		{"7 dias exatos ainda é recente", Facts{LastSeen: agora.Add(-StaleAfter), Pending: 1}, StateMissing, ""},
		{"parada há 9 dias, com o último resultado", Facts{LastSeen: agora.Add(-9 * 24 * time.Hour), Pending: 5, PendingUnit: "CVEs"}, StateStale, "sem contato há 9 dias (limite: 7)"},
		{"parada e sem inventário", Facts{LastSeen: agora.Add(-30 * 24 * time.Hour), NoInventory: true}, StateStale, "ainda não enviou inventário"},
	}
	for _, c := range casos {
		e, motivos := Classify(c.f, agora)
		if e != c.estado {
			t.Errorf("%s: %s, esperado %s (%v)", c.nome, e, c.estado, motivos)
		}
		if c.motivo != "" && !slices.ContainsFunc(motivos, func(m string) bool { return strings.Contains(m, c.motivo) }) {
			t.Errorf("%s: motivos %v sem %q", c.nome, motivos, c.motivo)
		}
		if c.estado == StateOK && len(motivos) != 0 {
			t.Errorf("%s: em dia sem motivos: %v", c.nome, motivos)
		}
	}
}

func TestOrdemERotulos(t *testing.T) {
	if StateMissing.Rank() >= StateReboot.Rank() || StateReboot.Rank() >= StateOK.Rank() || State("outro").Rank() != len(States) {
		t.Error("ordem")
	}
	for _, s := range States {
		if s.Label() == "" || strings.Contains(s.Label(), "_") {
			t.Errorf("%s: %q", s, s.Label())
		}
	}
}
