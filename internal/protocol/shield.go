package protocol

import (
	"errors"
	"time"
)

// PatchD Shield (docs/shield.md): a "Minha Proteção" da Central de software. O agente pede com
// a credencial da máquina e a identidade que leu do token do Windows (SelfServiceUser); o
// servidor responde com a pontuação da máquina e o progresso da pessoa. Nada aqui dá XP: o XP
// é decidido pelo servidor, a partir de evidência.

// ShieldPath é a rota do agente.
const ShieldPath = "/api/v1/agent/shield"

// CapabilityShield: o agente atende a operação "shield" do canal local (a Central mostra a aba).
const CapabilityShield = "shield-v1"

// ShieldView é a resposta.
type ShieldView struct {
	// Enabled: o Shield está ligado e esta máquina participa (falso: a Central esconde a aba).
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"` // por que não participa (desligado, fora do piloto)
	Brand   string `json:"brand,omitempty"`
	Mission string `json:"mission,omitempty"`
	Slogan  string `json:"slogan,omitempty"`

	Machine *ShieldMachineView `json:"machine,omitempty"`
	Player  *ShieldPlayerView  `json:"player,omitempty"`
	// Missions: as ativas e agendadas que valem para esta pessoa (coletivas do grupo dela).
	Missions []ShieldMissionView `json:"missions,omitempty"`
	// Notices: os avisos que a Central pode mostrar perto do relógio (o ID não se repete para o
	// mesmo fato: a Central não avisa duas vezes).
	Notices []ShieldNotice `json:"notices,omitempty"`
	// Notify: as regras dos avisos (a Central respeita).
	Notify ShieldNotify `json:"notify"`
}

// ShieldMachineView é a pontuação da máquina.
type ShieldMachineView struct {
	Score        *float64              `json:"score"`
	Display      int                   `json:"display"` // o número mostrado (nunca 100 sem o selo)
	Sealed       bool                  `json:"sealed"`
	Overall      string                `json:"overall"` // protegida, quase, atencao, risco, sem_dado
	OverallLabel string                `json:"overall_label"`
	Blockers     []string              `json:"blockers,omitempty"`
	Criteria     []ShieldCriterionView `json:"criteria"`
	EvaluatedAt  time.Time             `json:"evaluated_at"`
	Pending      bool                  `json:"pending,omitempty"` // ainda não avaliada
}

// ShieldCriterionView é um critério, com o texto pronto.
type ShieldCriterionView struct {
	Criterion   string     `json:"criterion"`
	Label       string     `json:"label"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"status_label"`
	Blame       string     `json:"blame,omitempty"`
	BlameLabel  string     `json:"blame_label,omitempty"`
	Detail      string     `json:"detail,omitempty"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	Weight      float64    `json:"weight"`
	Obtained    float64    `json:"obtained"`
	Mandatory   bool       `json:"mandatory,omitempty"`
	// Action: o que a pessoa pode fazer na própria Central ("reiniciar", "atualizar"); vazio: nada.
	Action string `json:"action,omitempty"`
}

// ShieldPlayerView é o progresso da pessoa.
type ShieldPlayerView struct {
	Streak        int               `json:"streak"`
	Best          int               `json:"best"`
	Protected     int               `json:"protected"`
	XP            int64             `json:"xp"`
	Level         string            `json:"level,omitempty"` // vazio: nenhum ainda
	NextLevel     string            `json:"next_level,omitempty"`
	NextLevelDays int               `json:"next_level_days,omitempty"` // faltam
	Badges        []ShieldBadgeView `json:"badges,omitempty"`
	Locked        []ShieldBadgeView `json:"locked,omitempty"` // as próximas a conquistar
	Recent        []ShieldXPView    `json:"recent,omitempty"`
}

// ShieldBadgeView é uma conquista.
type ShieldBadgeView struct {
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	EarnedAt    *time.Time `json:"earned_at,omitempty"`
}

// ShieldXPView é um evento de XP, para a pessoa ver de onde veio.
type ShieldXPView struct {
	Reason string    `json:"reason"`
	Points int       `json:"points"`
	Day    time.Time `json:"day"`
}

// ShieldMissionView é uma missão.
type ShieldMissionView struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Scope       string    `json:"scope"`
	Status      string    `json:"status"`
	Progress    string    `json:"progress"`
	Percent     int       `json:"percent"`
	XP          int       `json:"xp"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
}

// ShieldNotice é um aviso.
type ShieldNotice struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// ShieldNotify são as regras dos avisos.
type ShieldNotify struct {
	Enabled   bool `json:"enabled"`
	MinHours  int  `json:"min_hours"`
	WorkStart int  `json:"work_start"`
	WorkEnd   int  `json:"work_end"`
}

// Check confere os limites da resposta (a Central só desenha o que passa aqui).
func (v ShieldView) Check() error {
	if len(v.Missions) > 50 || len(v.Notices) > 20 || len(v.Brand) > 200 || len(v.Mission) > 200 || len(v.Slogan) > 400 {
		return errShieldView
	}
	if m := v.Machine; m != nil && (len(m.Criteria) > 20 || len(m.Blockers) > 20 || m.Display < 0 || m.Display > 100 || (m.Display == 100 && !m.Sealed)) {
		return errShieldView
	}
	if p := v.Player; p != nil && (len(p.Badges) > 200 || len(p.Locked) > 20 || len(p.Recent) > 50) {
		return errShieldView
	}
	return nil
}

var errShieldView = errors.New("resposta do Shield fora dos limites")
