package shield

import (
	"fmt"
	"time"
)

// DayVerdict é como um dia conta para a sequência de uma pessoa.
type DayVerdict string

const (
	DayProtected DayVerdict = "protegido" // todas as máquinas dela no selo: soma
	DayPause     DayVerdict = "pausa"     // faltou algo que não é dela (TI, automação, sem dado): não soma nem quebra
	DayBreak     DayVerdict = "quebra"    // pendência vencida que era dela: volta a zero
	DaySkip      DayVerdict = "ignorado"  // fim de semana, feriado, ou ela não usou máquina nenhuma
)

// MachineDay é o fim do dia de uma máquina (o último retrato do dia).
type MachineDay struct {
	MachineID string
	Sealed    bool
	UserFault bool
	NoData    bool
}

// Verdict decide o dia de uma pessoa pelas máquinas que ela usou nele. Uma máquina
// compartilhada conta para todos os que a usaram no dia; quem usou duas, só tem o dia
// protegido com as duas no selo.
func Verdict(p *Policy, day time.Time, machines []MachineDay) DayVerdict {
	if !p.Eligible(day) || len(machines) == 0 {
		return DaySkip
	}
	all := true
	for _, m := range machines {
		if m.UserFault {
			return DayBreak
		}
		if !m.Sealed {
			all = false
		}
	}
	if all {
		return DayProtected
	}
	return DayPause
}

// Streak é a sequência de uma pessoa.
type Streak struct {
	Current   int    `json:"current"`
	Best      int    `json:"best"`
	Protected int    `json:"protected"` // dias protegidos acumulados (o que dá o nível)
	Breaks    int    `json:"breaks"`
	Comebacks int    `json:"comebacks"` // sequências de 5 refeitas depois de uma quebra
	LastDay   string `json:"last_day"`  // o último dia já fechado (AAAA-MM-DD): fechar de novo não muda nada
}

// comebackDays: o tamanho da sequência que conta como "volta por cima".
const comebackDays = 5

// Step aplica o dia à sequência. Idempotente: um dia já fechado (ou anterior) não muda nada.
// Devolve se mudou.
func (s *Streak) Step(day time.Time, v DayVerdict) bool {
	key := day.Format(time.DateOnly)
	if s.LastDay != "" && key <= s.LastDay {
		return false
	}
	s.LastDay = key
	switch v {
	case DayProtected:
		s.Current++
		s.Protected++
		if s.Current > s.Best {
			s.Best = s.Current
		}
		if s.Current == comebackDays && s.Breaks > 0 {
			s.Comebacks++
		}
	case DayBreak:
		if s.Current > 0 {
			s.Breaks++
		}
		s.Current = 0
	}
	return true
}

// XPEvent é um evento de XP decidido pelo servidor. Kind e Ref formam a chave única: o mesmo
// evento gravado duas vezes (dois servidores, um reprocessamento) conta uma vez só.
type XPEvent struct {
	Kind   string
	Ref    string
	Points int
	Reason string
}

// Tipos de evento de XP.
const (
	XPProtectedDay = "dia_protegido"
	XPStreakBonus  = "bonus_sequencia"
	XPReboot       = "reinicio"
	XPUpdate       = "atualizacao"
	XPMission      = "missao"
	XPAchievement  = "conquista"
	XPAdjust       = "ajuste" // compensação do administrador, com motivo, auditada
)

// DayXP são os eventos do dia fechado: o dia protegido e o bônus de sequência, se chegou a
// um marco. O bônus leva a data de início da sequência na chave: refazer a mesma sequência
// não paga de novo, uma sequência nova paga.
func DayXP(p *Policy, day time.Time, v DayVerdict, after Streak) []XPEvent {
	if v != DayProtected {
		return nil
	}
	key := day.Format(time.DateOnly)
	out := []XPEvent{{Kind: XPProtectedDay, Ref: key, Points: p.XP.ProtectedDay, Reason: "dia útil 100% protegido"}}
	for _, b := range p.XP.StreakBonus {
		if after.Current == b.Days {
			out = append(out, XPEvent{Kind: XPStreakBonus, Ref: fmt.Sprintf("%d:%s", b.Days, key), Points: b.Points,
				Reason: fmt.Sprintf("%d dias úteis seguidos protegidos", b.Days)})
		}
	}
	return out
}

// CapDay aplica o teto diário: os eventos que passam do teto ficam com o que sobra (zero, no
// limite). already é o que a pessoa já ganhou no dia (sem os ajustes).
func CapDay(p *Policy, already int, ev []XPEvent) []XPEvent {
	if p.XP.MaxPerDay <= 0 {
		return ev
	}
	left := max(p.XP.MaxPerDay-already, 0)
	out := make([]XPEvent, 0, len(ev))
	for _, e := range ev {
		e.Points = min(e.Points, left)
		left -= e.Points
		out = append(out, e)
	}
	return out
}

// Counters são as medidas de uma pessoa para as conquistas.
type Counters map[Metric]int

// Earned devolve as conquistas que a pessoa tem direito e ainda não tem.
func Earned(p *Policy, c Counters, have map[string]bool) []Achievement {
	var out []Achievement
	for _, a := range p.Achievements {
		if !have[a.Code] && c[a.Metric] >= a.Min {
			out = append(out, a)
		}
	}
	return out
}
