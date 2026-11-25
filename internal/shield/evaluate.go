package shield

import (
	"fmt"
	"math"
	"time"
)

// Evidence é o que o servidor sabe de uma máquina, já tirado da avaliação de compliance, do
// inventário, da padronização e do check-in. Nada aqui vem do usuário.
type Evidence struct {
	LastSeen    time.Time
	Stale       bool       // sem contato há mais que o limite de compliance (7 dias)
	InventoryAt *time.Time // coleta do inventário avaliado; nil: nunca enviou

	// OSEvaluated: o sistema foi avaliado contra o catálogo (falso: sem catálogo, sem inventário).
	OSEvaluated bool
	OSPending   int // correções pendentes do sistema
	OSCritical  int // das pendentes, críticas ou com exploração conhecida

	TPKnown       bool // há inventário para conferir os programas
	TPOutdated    int  // desatualizados
	TPUnsupported int  // fora de suporte
	TPUserFixable int  // dos desatualizados, os que a Central oferece para o próprio usuário atualizar

	Reboot *bool // da última busca; nil: não soube dizer

	// StdApplies: há plano de padronização para a máquina; StdKnown: o resultado já chegou.
	StdApplies, StdKnown  bool
	StdFailed, StdWaiting int
	StdConflicts          int

	// AgentOutdated: nil quando não há versão publicada para comparar.
	AgentOutdated *bool
	AgentKnown    bool

	AntivirusDetail string
	Antivirus *bool // nil: sem evidência (o agente ainda não coleta)

	Exceptions map[Criterion]bool      // exceções aprovadas e vigentes
	Since      map[Criterion]time.Time // desde quando cada critério está fora (da avaliação anterior)
}

// CriterionResult é o resultado de um critério.
type CriterionResult struct {
	Criterion Criterion  `json:"criterion"`
	Status    Status     `json:"status"`
	Blame     Blame      `json:"blame,omitempty"`
	Weight    float64    `json:"weight"`
	Obtained  float64    `json:"obtained"`
	Mandatory bool       `json:"mandatory,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	Since     *time.Time `json:"since,omitempty"`    // fora desde
	Deadline  *time.Time `json:"deadline,omitempty"` // fim do prazo
}

// Overall é a faixa da máquina.
type Overall string

const (
	Protected Overall = "protegida" // selo 100%
	Near      Overall = "quase"     // NearMin ou mais
	Attention Overall = "atencao"   // AttentionMin ou mais
	Risk      Overall = "risco"     // abaixo
	NoData    Overall = "sem_dado"  // sem nada que conte, ou sem contato
	Off       Overall = "fora"      // fora do módulo (desligado ou fora do piloto)
)

// Label é o nome para pessoas.
func (o Overall) Label() string {
	switch o {
	case Protected:
		return "100% protegida"
	case Near:
		return "quase lá"
	case Attention:
		return "atenção"
	case Risk:
		return "em risco"
	case NoData:
		return "sem dados suficientes"
	}
	return "fora do Shield"
}

// Result é a avaliação de uma máquina.
type Result struct {
	// Score: 0 a 100 com duas casas; nil quando nenhum critério conta.
	Score *float64 `json:"score"`
	// Display: o número mostrado, sempre para baixo, e nunca 100 sem o selo.
	Display  int               `json:"display"`
	Sealed   bool              `json:"sealed"`
	Overall  Overall           `json:"overall"`
	Blockers []string          `json:"blockers,omitempty"` // o que impede o selo
	Criteria []CriterionResult `json:"criteria"`
	// UserFault: há pendência vencida que é do usuário (quebra a sequência).
	UserFault bool `json:"user_fault,omitempty"`
	// Blame: a responsabilidade principal do que falta (a do maior peso perdido).
	Blame Blame `json:"blame,omitempty"`
}

// SinceMap devolve o "fora desde" de cada critério, para a próxima avaliação.
func (r Result) SinceMap() map[Criterion]time.Time {
	out := map[Criterion]time.Time{}
	for _, c := range r.Criteria {
		if c.Since != nil {
			out[c.Criterion] = *c.Since
		}
	}
	return out
}

// Evaluate calcula a pontuação: 100 × Σ obtidos / Σ aplicáveis. O desconhecido conta no
// denominador e não soma (nunca 100% sem prova); o não aplicável e a exceção ficam de fora.
func Evaluate(p *Policy, e Evidence, now time.Time) Result {
	var res Result
	var sum, got float64
	lost := map[Blame]float64{}
	for _, c := range Criteria {
		cp := p.Criteria[c]
		if !cp.Enabled {
			continue
		}
		cr := judge(p, c, cp, e, now)
		cr.Criterion, cr.Weight, cr.Mandatory = c, cp.Weight, cp.Mandatory
		if cr.Status != Conforme && cr.Status != NaoAplicavel && e.Exceptions[c] {
			cr.Status, cr.Blame = Excecao, BlameNone
			cr.Detail = "exceção aprovada pela TI: " + cr.Detail
		}
		if cr.Status.Counted() {
			cr.Obtained = cp.Weight * p.Credits[cr.Status]
			sum += cp.Weight
			got += cr.Obtained
			if cr.Obtained < cp.Weight {
				lost[cr.Blame] += cp.Weight - cr.Obtained
			}
		}
		if cr.Status == AguardandoUsuario || (cr.Status == NaoConforme && cr.Blame == BlameUser) {
			res.UserFault = true
		}
		if cp.Mandatory && cr.Status != Conforme && cr.Status != NaoAplicavel {
			res.Blockers = append(res.Blockers, fmt.Sprintf("%s: %s", c.Label(), cr.Status.Label()))
		}
		res.Criteria = append(res.Criteria, cr)
	}
	if e.OSCritical > 0 {
		res.Blockers = append(res.Blockers, fmt.Sprintf("%d correção(ões) crítica(s) ou com exploração conhecida pendente(s)", e.OSCritical))
	}
	if e.Reboot != nil && *e.Reboot {
		res.Blockers = appendOnce(res.Blockers, "reinício pendente")
	}
	if e.Stale {
		res.Blockers = append(res.Blockers, "sem contato recente com o servidor")
	}
	best := 0.0
	for b, w := range lost {
		if w > best || (w == best && blameRank(b) < blameRank(res.Blame)) {
			best, res.Blame = w, b
		}
	}
	if sum <= 0 {
		res.Overall = NoData
		return res
	}
	score := math.Round(got/sum*10000) / 100
	res.Score = &score
	res.Sealed = got >= sum && len(res.Blockers) == 0
	res.Display = int(math.Floor(score))
	if !res.Sealed && res.Display >= 100 {
		res.Display = 99
	}
	switch {
	case e.Stale:
		res.Overall = NoData
	case res.Sealed:
		res.Overall = Protected
	case res.Display >= p.NearMin:
		res.Overall = Near
	case res.Display >= p.AttentionMin:
		res.Overall = Attention
	default:
		res.Overall = Risk
	}
	return res
}

func appendOnce(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

// blameRank desempata a responsabilidade principal: o que o usuário pode fazer vem primeiro.
func blameRank(b Blame) int {
	switch b {
	case BlameUser:
		return 0
	case BlameIT:
		return 1
	case BlameAuto:
		return 2
	case BlameExternal:
		return 3
	case BlameUnknown:
		return 4
	}
	return 5
}

// judge decide um critério. Since e Deadline saem preenchidos quando o critério está fora.
func judge(p *Policy, c Criterion, cp CriterionPolicy, e Evidence, now time.Time) (cr CriterionResult) {
	// O prazo conta de quando o critério saiu (da avaliação anterior; senão, agora).
	since := now
	if t, ok := e.Since[c]; ok && !t.After(now) {
		since = t
	}
	grace := func(days int) bool {
		d := since.Add(time.Duration(days) * 24 * time.Hour)
		cr.Deadline = &d
		return now.Before(d)
	}
	defer func() {
		if cr.Status != Conforme && cr.Status != NaoAplicavel {
			cr.Since = &since
		} else {
			cr.Deadline = nil
		}
	}()
	if e.Stale && c != CritAntivirus {
		days := int(now.Sub(e.LastSeen).Hours() / 24)
		return CriterionResult{Status: Desconhecido, Blame: BlameExternal, Detail: fmt.Sprintf("sem contato há %d dias", days)}
	}
	switch c {
	case CritOS:
		switch {
		case e.InventoryAt == nil:
			return CriterionResult{Status: Desconhecido, Blame: BlameAuto, Detail: "a máquina ainda não enviou inventário"}
		case !e.OSEvaluated:
			return CriterionResult{Status: Desconhecido, Blame: BlameIT, Detail: "o sistema não pôde ser avaliado (sem catálogo para esta versão)"}
		case e.OSPending == 0:
			return CriterionResult{Status: Conforme, Detail: "sistema em dia"}
		}
		cr.Detail = fmt.Sprintf("%d correção(ões) pendente(s)", e.OSPending)
		days := cp.GraceDays
		if e.OSCritical > 0 {
			days = min(days, p.CriticalGraceDays)
			cr.Detail += fmt.Sprintf(", %d crítica(s)", e.OSCritical)
		}
		if grace(days) {
			cr.Status, cr.Blame = DentroPrazo, BlameAuto
			return cr
		}
		cr.Status, cr.Blame = AguardandoTI, BlameIT
		if e.OSCritical > 0 {
			cr.Status = NaoConforme
		}
		return cr
	case CritThirdParty:
		switch {
		case !e.TPKnown:
			return CriterionResult{Status: Desconhecido, Blame: BlameAuto, Detail: "sem inventário dos programas"}
		case e.TPUnsupported > 0:
			cr.Status, cr.Blame = NaoConforme, BlameIT
			cr.Detail = fmt.Sprintf("%d programa(s) fora de suporte", e.TPUnsupported)
			if e.TPOutdated > 0 {
				cr.Detail += fmt.Sprintf(" e %d desatualizado(s)", e.TPOutdated)
			}
			grace(cp.GraceDays)
			return cr
		case e.TPOutdated == 0:
			return CriterionResult{Status: Conforme, Detail: "programas em dia"}
		}
		cr.Detail = fmt.Sprintf("%d programa(s) desatualizado(s)", e.TPOutdated)
		switch {
		case grace(cp.GraceDays):
			cr.Status, cr.Blame = DentroPrazo, BlameAuto
		case e.TPUserFixable > 0:
			cr.Status, cr.Blame = AguardandoUsuario, BlameUser
			cr.Detail += fmt.Sprintf(" (%d com atualização na Central para você)", e.TPUserFixable)
		default:
			cr.Status, cr.Blame = AguardandoTI, BlameIT
		}
		return cr
	case CritReboot:
		switch {
		case e.Reboot == nil:
			return CriterionResult{Status: Desconhecido, Blame: BlameUnknown, Detail: "a última busca não disse se há reinício pendente"}
		case !*e.Reboot:
			return CriterionResult{Status: Conforme, Detail: "sem reinício pendente"}
		}
		cr.Detail = "uma atualização espera o reinício do computador"
		if grace(cp.GraceDays) {
			cr.Status, cr.Blame = DentroPrazo, BlameUser
		} else {
			cr.Status, cr.Blame = AguardandoUsuario, BlameUser
		}
		return cr
	case CritStandard:
		switch {
		case !e.StdApplies:
			return CriterionResult{Status: NaoAplicavel, Detail: "nenhuma padronização para esta máquina"}
		case !e.StdKnown:
			return CriterionResult{Status: Desconhecido, Blame: BlameAuto, Detail: "a padronização ainda não informou o resultado"}
		case e.StdFailed > 0:
			cr.Status, cr.Blame = NaoConforme, BlameIT
			cr.Detail = fmt.Sprintf("%d item(ns) da padronização falharam", e.StdFailed)
			grace(cp.GraceDays)
			return cr
		case e.StdConflicts > 0:
			cr.Status, cr.Blame = Parcial, BlameIT
			cr.Detail = fmt.Sprintf("%d atalho(s) em conflito", e.StdConflicts)
			grace(cp.GraceDays)
			return cr
		case e.StdWaiting == 0:
			return CriterionResult{Status: Conforme, Detail: "padronização aplicada"}
		}
		cr.Detail = fmt.Sprintf("%d item(ns) da padronização na fila", e.StdWaiting)
		if grace(cp.GraceDays) {
			cr.Status, cr.Blame = DentroPrazo, BlameAuto
		} else {
			cr.Status, cr.Blame = AguardandoTI, BlameAuto
		}
		return cr
	case CritInventory:
		if e.InventoryAt == nil {
			return CriterionResult{Status: Desconhecido, Blame: BlameAuto, Detail: "a máquina ainda não enviou inventário"}
		}
		age := now.Sub(*e.InventoryAt)
		days := int(age.Hours() / 24)
		switch {
		case age <= time.Duration(p.InventoryDays)*24*time.Hour:
			return CriterionResult{Status: Conforme, Detail: "inventário recente"}
		case age <= time.Duration(p.InventoryHardDays)*24*time.Hour:
			return CriterionResult{Status: Parcial, Blame: BlameAuto, Detail: fmt.Sprintf("inventário de %d dias atrás", days)}
		}
		return CriterionResult{Status: NaoConforme, Blame: BlameExternal, Detail: fmt.Sprintf("inventário de %d dias atrás", days)}
	case CritAgent:
		switch {
		case e.AgentOutdated == nil:
			return CriterionResult{Status: NaoAplicavel, Detail: "sem versão publicada para comparar"}
		case !e.AgentKnown:
			return CriterionResult{Status: Desconhecido, Blame: BlameAuto, Detail: "versão do agente desconhecida"}
		case !*e.AgentOutdated:
			return CriterionResult{Status: Conforme, Detail: "agente na versão publicada"}
		}
		cr.Detail = "agente abaixo da versão publicada"
		if grace(cp.GraceDays) {
			cr.Status, cr.Blame = DentroPrazo, BlameAuto
		} else {
			cr.Status, cr.Blame = AguardandoTI, BlameAuto
		}
		return cr
	case CritAntivirus:
		if e.AntivirusDetail != "" {
			if e.Antivirus == nil { return CriterionResult{Status: Desconhecido, Blame: BlameUnknown, Detail: e.AntivirusDetail} }
			if *e.Antivirus { return CriterionResult{Status: Conforme, Detail: e.AntivirusDetail} }
			return CriterionResult{Status: NaoConforme, Blame: BlameIT, Detail: e.AntivirusDetail}
		}
		switch {
		case e.Antivirus == nil:
			return CriterionResult{Status: Desconhecido, Blame: BlameUnknown, Detail: "o agente não informa o antivírus"}
		case *e.Antivirus:
			return CriterionResult{Status: Conforme, Detail: "antivírus ativo"}
		}
		return CriterionResult{Status: NaoConforme, Blame: BlameIT, Detail: "antivírus desligado"}
	}
	return CriterionResult{Status: Desconhecido, Blame: BlameUnknown}
}
