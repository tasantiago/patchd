package compliance

import (
	"fmt"
	"time"
)

// State é o estado de compliance de uma máquina (RF-23). Os valores são estáveis: vão
// para a API e para as exportações.
type State string

const (
	StateOK      State = "em_dia"           // avaliada, sem pendência, sem reinício pendente
	StateMissing State = "faltando"         // correção pendente (ou major do macOS sem suporte)
	StateReboot  State = "reboot_pendente"  // correções aplicadas que só valem depois do reinício
	StateStale   State = "sem_dado_recente" // sem contato há mais de StaleAfter (RNF-12)
	StateUnknown State = "desconhecido"     // sem inventário, ou sem catálogo para avaliar
)

// States é a ordem de exibição: o que pede ação primeiro.
var States = []State{StateMissing, StateReboot, StateStale, StateUnknown, StateOK}

// Label é o nome para pessoas.
func (s State) Label() string {
	switch s {
	case StateOK:
		return "em dia"
	case StateMissing:
		return "faltando"
	case StateReboot:
		return "reboot pendente"
	case StateStale:
		return "sem dado recente"
	}
	return "desconhecido"
}

// Rank é a posição em States (para ordenar a frota).
func (s State) Rank() int {
	for i, x := range States {
		if x == s {
			return i
		}
	}
	return len(States)
}

// StaleAfter: sem check-in válido por mais que isso, a máquina fica "sem dado recente"
// (RNF-12, padrão de 7 dias). O agente faz check-in a cada hora (RNF-03): uma semana de
// silêncio é máquina desligada, desinstalada ou com o agente quebrado.
const StaleAfter = 7 * 24 * time.Hour

// Facts é o que a classificação precisa saber de uma máquina, já extraído da avaliação.
type Facts struct {
	LastSeen      time.Time // último contato de qualquer tipo
	NoInventory   bool      // nunca enviou inventário
	Unevaluated   string    // por que o SO não foi avaliado (sem catálogo, versão fora dele)
	Pending       int       // pacotes (Linux) ou CVEs (Windows, macOS) com correção pendente
	PendingUnit   string    // "pacotes" ou "CVEs"
	Exploited     int       // dos pendentes, quantos com exploração conhecida (KEV, MSRC) ou inferida
	SupportEnded  bool      // macOS: a major não recebe mais correções
	RebootPending *bool     // da última busca: nil = a busca não soube dizer
	NewerKernel   bool      // Linux: kernel mais novo instalado e não carregado
}

// Classify aplica o RF-23 com o P-02 (compliance = resultado bom + dado fresco):
//  1. sem contato há mais de StaleAfter: sem dado recente, seja qual for o último resultado;
//  2. sem inventário ou sem avaliação: desconhecido;
//  3. correção pendente ou major sem suporte: faltando;
//  4. reinício pendente (pela busca ou pelo kernel): reboot pendente;
//  5. senão: em dia.
//
// Os motivos trazem tudo o que vale saber, inclusive o que não decidiu o estado (um
// "faltando" com reinício pendente diz as duas coisas).
func Classify(f Facts, now time.Time) (State, []string) {
	var reasons []string
	stale := !f.LastSeen.IsZero() && now.Sub(f.LastSeen) > StaleAfter
	if stale {
		reasons = append(reasons, fmt.Sprintf("sem contato há %d dias (limite: %d)",
			int(now.Sub(f.LastSeen).Hours()/24), int(StaleAfter.Hours()/24)))
	}
	if f.NoInventory {
		reasons = append(reasons, "a máquina ainda não enviou inventário")
		if stale {
			return StateStale, reasons
		}
		return StateUnknown, reasons
	}
	if f.Unevaluated != "" {
		reasons = append(reasons, "sem avaliação: "+f.Unevaluated)
	}
	if f.Pending > 0 {
		unit := f.PendingUnit
		if unit == "" {
			unit = "itens"
		}
		reasons = append(reasons, fmt.Sprintf("%d %s com correção pendente (%d com exploração conhecida)", f.Pending, unit, f.Exploited))
	}
	if f.SupportEnded {
		reasons = append(reasons, "a versão principal do sistema não recebe mais correções")
	}
	reboot := (f.RebootPending != nil && *f.RebootPending) || f.NewerKernel
	if f.RebootPending != nil && *f.RebootPending {
		reasons = append(reasons, "reinício pendente (informado pela busca do agente)")
	}
	if f.NewerKernel {
		reasons = append(reasons, "kernel mais novo instalado e ainda não carregado")
	}

	switch {
	case stale:
		return StateStale, reasons
	case f.Unevaluated != "":
		return StateUnknown, reasons
	case f.Pending > 0 || f.SupportEnded:
		return StateMissing, reasons
	case reboot:
		return StateReboot, reasons
	}
	return StateOK, reasons
}
