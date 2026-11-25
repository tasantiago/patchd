// Package shield é o Atende+ Shield — Missão 100% (docs/shield.md): a pontuação de proteção de
// cada máquina (0 a 100%), o selo 100%, a sequência de dias protegidos, o XP, os níveis, as
// conquistas e as missões. Este pacote é puro (sem banco e sem rede): recebe as evidências que
// o servidor já tem (compliance, inventário, padronização, versão do agente) e a política, e
// devolve o resultado. Quem grava, quando e com qual trava é o servidor (internal/api).
//
// Princípios: a pontuação nunca mostra 100% sem prova (o que não se sabe vale zero e impede o
// selo), é independente do estado de compliance (que continua sendo o da auditoria) e tudo o
// que dá XP é decidido aqui, do lado do servidor, a partir de evidência — nunca do cliente.
package shield

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Criterion é um critério da pontuação. Os valores são estáveis: vão para o banco e para a API.
type Criterion string

const (
	CritOS         Criterion = "so"           // atualizações do sistema operacional
	CritThirdParty Criterion = "terceiros"    // programas de terceiros
	CritReboot     Criterion = "reinicio"     // sem reinício pendente
	CritStandard   Criterion = "padronizacao" // padronização (atalhos, programas padrão)
	CritInventory  Criterion = "inventario"   // inventário atualizado
	CritAgent      Criterion = "agente"       // agente na versão publicada
	CritAntivirus  Criterion = "antivirus"    // antivírus ativo (o agente ainda não coleta: desligado)
)

// Criteria é a ordem de exibição.
var Criteria = []Criterion{CritOS, CritThirdParty, CritReboot, CritStandard, CritInventory, CritAgent, CritAntivirus}

// Label é o nome para pessoas.
func (c Criterion) Label() string {
	switch c {
	case CritOS:
		return "Atualizações do sistema"
	case CritThirdParty:
		return "Programas atualizados"
	case CritReboot:
		return "Sem reinício pendente"
	case CritStandard:
		return "Padronização"
	case CritInventory:
		return "Inventário em dia"
	case CritAgent:
		return "Agente atualizado"
	case CritAntivirus:
		return "Antivírus ativo"
	}
	return string(c)
}

// Valid diz se é um dos critérios conhecidos.
func (c Criterion) Valid() bool { return slices.Contains(Criteria, c) }

// Status é a situação de um critério numa máquina.
type Status string

const (
	Conforme          Status = "conforme"
	Parcial           Status = "parcial"
	NaoConforme       Status = "nao_conforme"
	Desconhecido      Status = "desconhecido"
	NaoAplicavel      Status = "nao_aplicavel"
	DentroPrazo       Status = "dentro_do_prazo"
	AguardandoUsuario Status = "aguardando_usuario"
	AguardandoTI      Status = "aguardando_ti"
	Excecao           Status = "excecao"
)

// Statuses: todos, na ordem do que pede ação primeiro.
var Statuses = []Status{NaoConforme, AguardandoUsuario, AguardandoTI, Parcial, DentroPrazo, Desconhecido, Excecao, NaoAplicavel, Conforme}

// Label é o nome para pessoas.
func (s Status) Label() string {
	switch s {
	case Conforme:
		return "conforme"
	case Parcial:
		return "parcial"
	case NaoConforme:
		return "não conforme"
	case Desconhecido:
		return "desconhecido"
	case NaoAplicavel:
		return "não se aplica"
	case DentroPrazo:
		return "dentro do prazo"
	case AguardandoUsuario:
		return "aguardando você"
	case AguardandoTI:
		return "aguardando a TI"
	case Excecao:
		return "exceção aprovada"
	}
	return string(s)
}

// Counted: entra no denominador da pontuação (não aplicável e exceção ficam de fora).
func (s Status) Counted() bool { return s != NaoAplicavel && s != Excecao }

// Blame é de quem é a ação que falta (a responsabilidade).
type Blame string

const (
	BlameNone     Blame = ""
	BlameUser     Blame = "usuario"
	BlameIT       Blame = "ti"
	BlameAuto     Blame = "automacao"
	BlameExternal Blame = "externa"
	BlameUnknown  Blame = "indeterminada"
)

// Label é o nome para pessoas.
func (b Blame) Label() string {
	switch b {
	case BlameUser:
		return "usuário"
	case BlameIT:
		return "equipe de TI"
	case BlameAuto:
		return "automação do patchd"
	case BlameExternal:
		return "fator externo"
	case BlameUnknown:
		return "indeterminada"
	}
	return "—"
}

// CriterionPolicy é a configuração de um critério.
type CriterionPolicy struct {
	Enabled   bool    `json:"enabled"`
	Weight    float64 `json:"weight"`     // peso relativo (0 a 100)
	Mandatory bool    `json:"mandatory"`  // obrigatório para o selo 100%
	GraceDays int     `json:"grace_days"` // prazo, em dias corridos, antes de virar pendência
}

// Level é um nível, pelos dias protegidos acumulados.
type Level struct {
	Name    string `json:"name"`
	MinDays int    `json:"min_days"`
}

// Achievement é uma conquista declarativa: ganha quem tem Metric >= Min.
type Achievement struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Metric      Metric `json:"metric"`
	Min         int    `json:"min"`
	XP          int    `json:"xp"` // XP da conquista (uma vez)
}

// Metric é o que uma conquista mede.
type Metric string

const (
	MetricProtectedDays Metric = "dias_protegidos" // dias protegidos acumulados
	MetricStreak        Metric = "sequencia"       // sequência atual
	MetricBestStreak    Metric = "melhor_sequencia"
	MetricXP            Metric = "xp"
	MetricReboots       Metric = "reinicios"    // reinícios feitos quando havia reinício pendente
	MetricMissions      Metric = "missoes"      // missões individuais cumpridas
	MetricComebacks     Metric = "retomadas"    // sequências de 5 dias refeitas depois de uma quebra
	MetricSealedDays    Metric = "dias_selo"    // dias com todas as máquinas no selo (o mesmo que protegidos, hoje)
	MetricUpdates       Metric = "atualizacoes" // atualizações pedidas pela Central e concluídas
)

// Metrics: as conhecidas.
var Metrics = []Metric{MetricProtectedDays, MetricStreak, MetricBestStreak, MetricXP, MetricReboots, MetricMissions, MetricComebacks, MetricSealedDays, MetricUpdates}

// XPRules: quanto vale cada evento. Os eventos são do servidor (evidência), nunca do cliente.
type XPRules struct {
	ProtectedDay int           `json:"protected_day"` // dia útil protegido
	Reboot       int           `json:"reboot"`        // reiniciou quando havia reinício pendente (uma vez por dia por máquina)
	Update       int           `json:"update"`        // atualização pedida pela Central e concluída
	MaxPerDay    int           `json:"max_per_day"`   // teto por dia (sem os ajustes do administrador)
	StreakBonus  []StreakBonus `json:"streak_bonus"`
}

// StreakBonus: ao chegar a Days dias seguidos, Points a mais.
type StreakBonus struct {
	Days   int `json:"days"`
	Points int `json:"points"`
}

// Brand: os textos do módulo, configuráveis (sem nome de instituição no código).
type Brand struct {
	Name    string `json:"name"`
	Mission string `json:"mission"`
	Slogan  string `json:"slogan"`
}

// Ranking: o que aparece no ranking.
type Ranking struct {
	Collective bool `json:"collective"` // por grupo de implantação (o padrão)
	Individual bool `json:"individual"` // por pessoa: desligado por padrão
	MinGroup   int  `json:"min_group"`  // grupos menores que isso não aparecem (privacidade)
	// Os rankings por lugar (places.go): o prédio e a sala vêm do patrimônio; o andar é a
	// faixa de IP, nos prédios que têm máquinas em mais de uma faixa. Prédio e andar usam
	// MinGroup; a sala, MinRoom (salas são pequenas, mas uma sala de 1 ou 2 é uma pessoa).
	Buildings bool `json:"buildings"`
	Floors    bool `json:"floors"`
	Rooms     bool `json:"rooms"`
	MinRoom   int  `json:"min_room"`
	// FloorNames: o nome de cada faixa de IP como andar ("10.1.2.0/24" → "2º andar"). A
	// Central nunca mostra a faixa: sem nome, o andar aparece como "Faixa N" do prédio.
	FloorNames map[string]string `json:"floor_names,omitempty"`
}

// Notify: os avisos da Central.
type Notify struct {
	Enabled   bool `json:"enabled"`
	MinHours  int  `json:"min_hours"`  // no máximo um aviso do Shield a cada tantas horas
	WorkStart int  `json:"work_start"` // só no horário de expediente (hora local)
	WorkEnd   int  `json:"work_end"`
}

// Policy é a política do Shield. Versionada no banco: cada gravação é uma versão nova.
type Policy struct {
	AntivirusServices AntivirusServices `json:"antivirus_services"`
	Enabled    bool                          `json:"enabled"`     // a chave geral do módulo
	PilotRings []int                         `json:"pilot_rings"` // vazio: todos os grupos; senão, só estes
	Criteria   map[Criterion]CriterionPolicy `json:"criteria"`
	Credits    map[Status]float64            `json:"credits"` // fração do peso que cada situação vale
	// InventoryDays: inventário mais velho que isso é parcial; InventoryHardDays, não conforme.
	InventoryDays     int `json:"inventory_days"`
	InventoryHardDays int `json:"inventory_hard_days"`
	// CriticalGraceDays: o prazo das correções críticas ou com exploração conhecida.
	CriticalGraceDays int `json:"critical_grace_days"`
	// NearMin e AttentionMin: as faixas de cor (quase lá, atenção; abaixo, risco).
	NearMin      int `json:"near_min"`
	AttentionMin int `json:"attention_min"`
	// Workdays: os dias da semana que contam na sequência (0 = domingo); Holidays, as datas
	// (AAAA-MM-DD) que não contam; Timezone, o fuso (vazio: o do painel).
	Workdays     []int         `json:"workdays"`
	Holidays     []string      `json:"holidays"`
	Timezone     string        `json:"timezone"`
	Levels       []Level       `json:"levels"`
	Achievements []Achievement `json:"achievements"`
	XP           XPRules       `json:"xp"`
	Brand        Brand         `json:"brand"`
	Ranking      Ranking       `json:"ranking"`
	Notify       Notify        `json:"notify"`
}

// OldDefaultName: o nome padrão do módulo até a troca para "Atende+ Shield".
const OldDefaultName = "PatchD Shield"

// Default é a política inicial: desligada (o administrador liga, de preferência para um grupo
// piloto), com os pesos da especificação. O antivírus fica desligado: o agente ainda não coleta
// essa evidência, e um critério sem evidência impediria o selo de todo o parque.
func Default() Policy {
	return Policy{
		Enabled: false,
		Criteria: map[Criterion]CriterionPolicy{
			CritOS:         {Enabled: true, Weight: 30, Mandatory: true, GraceDays: 7},
			CritThirdParty: {Enabled: true, Weight: 25, Mandatory: true, GraceDays: 7},
			CritReboot:     {Enabled: true, Weight: 15, Mandatory: true, GraceDays: 1},
			CritStandard:   {Enabled: true, Weight: 15, GraceDays: 2},
			CritInventory:  {Enabled: true, Weight: 5},
			CritAgent:      {Enabled: true, Weight: 5, GraceDays: 7},
			CritAntivirus:  {Enabled: false, Weight: 5},
		},
		Credits: map[Status]float64{
			Conforme: 1, Parcial: 0.5, DentroPrazo: 0.75, AguardandoTI: 0.5, AguardandoUsuario: 0.25,
			NaoConforme: 0, Desconhecido: 0,
		},
		InventoryDays: 3, InventoryHardDays: 7, CriticalGraceDays: 2,
		NearMin: 90, AttentionMin: 70,
		Workdays: []int{1, 2, 3, 4, 5},
		Levels: []Level{
			{"Bronze — Protetor", 1}, {"Prata — Sentinela", 7}, {"Ouro — Guardião", 15},
			{"Platina — Escudo de Elite", 30}, {"Diamante — Guardião Digital", 90},
			{"Mestre", 180}, {"Lendário", 365},
		},
		Achievements: DefaultAchievements(),
		XP: XPRules{ProtectedDay: 10, Reboot: 15, Update: 5, MaxPerDay: 60,
			StreakBonus: []StreakBonus{{5, 20}, {10, 40}, {22, 80}, {66, 200}}},
		Brand:   Brand{Name: "Atende+ Shield", Mission: "Missão 100%", Slogan: "Sua máquina protegida. Nosso ambiente mais seguro."},
		Ranking: Ranking{Collective: true, Individual: false, MinGroup: 5, Buildings: true, Floors: true, Rooms: true, MinRoom: 3},
		Notify:  Notify{Enabled: true, MinHours: 4, WorkStart: 8, WorkEnd: 18},
	}
}

// DefaultAchievements são as 13 conquistas iniciais.
func DefaultAchievements() []Achievement {
	return []Achievement{
		{"primeiro-escudo", "Primeiro escudo", "Primeiro dia útil com a máquina 100% protegida.", MetricProtectedDays, 1, 10},
		{"semana-blindada", "Semana blindada", "5 dias úteis seguidos protegidos.", MetricStreak, 5, 20},
		{"quinzena-segura", "Quinzena segura", "10 dias úteis seguidos protegidos.", MetricStreak, 10, 30},
		{"mes-de-ferro", "Mês de ferro", "22 dias úteis seguidos protegidos.", MetricStreak, 22, 60},
		{"trimestre-imbativel", "Trimestre imbatível", "66 dias úteis seguidos protegidos.", MetricStreak, 66, 150},
		{"reinicio-na-hora", "Reinício na hora", "Reiniciou quando a atualização pediu.", MetricReboots, 1, 10},
		{"reinicio-exemplar", "Reinício exemplar", "10 reinícios feitos quando a atualização pediu.", MetricReboots, 10, 40},
		{"missao-cumprida", "Missão cumprida", "Cumpriu a primeira missão.", MetricMissions, 1, 20},
		{"veterano-de-missoes", "Veterano de missões", "Cumpriu 5 missões.", MetricMissions, 5, 60},
		{"volta-por-cima", "Volta por cima", "Refez 5 dias seguidos depois de perder a sequência.", MetricComebacks, 1, 20},
		{"centuriao", "Centurião", "100 dias protegidos.", MetricProtectedDays, 100, 100},
		{"atualizador", "Atualizador", "Atualizou um programa pela Central.", MetricUpdates, 1, 10},
		{"mil-xp", "Mil de XP", "Chegou a 1.000 XP.", MetricXP, 1000, 0},
	}
}

var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

// Validate confere a política inteira; o erro diz o que corrigir.
func (p *Policy) Validate() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	total := 0.0
	if err := p.AntivirusServices.Validate(); err != nil { errs = append(errs, err) }
	for c, cp := range p.Criteria {
		if !c.Valid() {
			bad("critério desconhecido %q", c)
			continue
		}
		if cp.Weight < 0 || cp.Weight > 100 {
			bad("%s: peso de 0 a 100", c.Label())
		}
		if cp.GraceDays < 0 || cp.GraceDays > 90 {
			bad("%s: prazo de 0 a 90 dias", c.Label())
		}
		if cp.Enabled {
			total += cp.Weight
		}
	}
	if total <= 0 {
		bad("ligue ao menos um critério com peso maior que zero")
	}
	for s, v := range p.Credits {
		if !slices.Contains(Statuses, s) || !s.Counted() {
			bad("crédito para situação que não conta: %q", s)
		}
		if v < 0 || v > 1 {
			bad("crédito de %s: de 0 a 1", s.Label())
		}
	}
	// O que é fixo: conforme vale tudo; não conforme e desconhecido não valem nada (o que não
	// se sabe nunca soma). Ninguém consegue, pela política, dar 100% sem prova.
	if p.Credits[Conforme] != 1 || p.Credits[NaoConforme] != 0 || p.Credits[Desconhecido] != 0 {
		bad("conforme vale 1; não conforme e desconhecido valem 0")
	}
	for _, s := range []Status{Parcial, DentroPrazo, AguardandoTI, AguardandoUsuario} {
		if p.Credits[s] >= 1 {
			bad("%s precisa valer menos que conforme", s.Label())
		}
	}
	if p.InventoryDays < 1 || p.InventoryHardDays < p.InventoryDays || p.InventoryHardDays > 60 {
		bad("inventário: dias de 1 a 60, o limite de não conforme maior ou igual ao de parcial")
	}
	if p.CriticalGraceDays < 0 || p.CriticalGraceDays > 30 {
		bad("prazo das críticas: de 0 a 30 dias")
	}
	if p.AttentionMin < 0 || p.NearMin <= p.AttentionMin || p.NearMin > 99 {
		bad("faixas: atenção abaixo de quase lá, e quase lá até 99")
	}
	if len(p.Workdays) == 0 {
		bad("escolha ao menos um dia da semana que conta")
	}
	for _, d := range p.Workdays {
		if d < 0 || d > 6 {
			bad("dia da semana de 0 (domingo) a 6 (sábado)")
		}
	}
	if len(p.Holidays) > 400 {
		bad("feriados demais (até 400)")
	}
	for _, h := range p.Holidays {
		if _, err := time.Parse(time.DateOnly, h); err != nil {
			bad("feriado %q fora do formato AAAA-MM-DD", h)
		}
	}
	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			bad("fuso desconhecido %q", p.Timezone)
		}
	}
	for _, r := range p.PilotRings {
		if r < 0 || r > 9 {
			bad("grupo piloto fora da faixa: %d", r)
		}
	}
	if len(p.Levels) == 0 || len(p.Levels) > 20 {
		bad("de 1 a 20 níveis")
	}
	for i, l := range p.Levels {
		if err := text(l.Name, 40); err != nil {
			bad("nível %d: %v", i+1, err)
		}
		if l.MinDays < 1 || (i > 0 && l.MinDays <= p.Levels[i-1].MinDays) {
			bad("nível %q: os dias precisam crescer a partir de 1", l.Name)
		}
	}
	if len(p.Achievements) > 100 {
		bad("conquistas demais (até 100)")
	}
	seen := map[string]bool{}
	for _, a := range p.Achievements {
		switch {
		case !codeRe.MatchString(a.Code):
			bad("conquista %q: código de 2 a 40 letras minúsculas, números e hífens", a.Code)
		case seen[a.Code]:
			bad("conquista repetida %q", a.Code)
		case !slices.Contains(Metrics, a.Metric):
			bad("conquista %q: medida desconhecida %q", a.Code, a.Metric)
		case a.Min < 1 || a.Min > 1_000_000 || a.XP < 0 || a.XP > 10_000:
			bad("conquista %q: mínimo de 1 a 1.000.000 e XP de 0 a 10.000", a.Code)
		}
		if err := text(a.Name, 60); err != nil {
			bad("conquista %q: %v", a.Code, err)
		}
		if err := text(a.Description, 200); err != nil {
			bad("conquista %q: %v", a.Code, err)
		}
		seen[a.Code] = true
	}
	x := p.XP
	if x.ProtectedDay < 0 || x.Reboot < 0 || x.Update < 0 || x.ProtectedDay > 1000 || x.Reboot > 1000 || x.Update > 1000 {
		bad("XP por evento de 0 a 1.000")
	}
	if x.MaxPerDay < 0 || x.MaxPerDay > 10_000 {
		bad("teto diário de XP de 0 a 10.000")
	}
	for i, b := range x.StreakBonus {
		if b.Days < 2 || b.Points < 0 || b.Points > 10_000 || (i > 0 && b.Days <= x.StreakBonus[i-1].Days) {
			bad("bônus de sequência: dias crescentes a partir de 2 e pontos de 0 a 10.000")
			break
		}
	}
	for _, t := range []struct {
		name, v string
		max     int
	}{{"nome do módulo", p.Brand.Name, 40}, {"nome da missão", p.Brand.Mission, 40}, {"slogan", p.Brand.Slogan, 120}} {
		if err := text(t.v, t.max); err != nil {
			bad("%s: %v", t.name, err)
		}
	}
	if p.Ranking.MinGroup < 1 || p.Ranking.MinGroup > 1000 {
		bad("tamanho mínimo do grupo no ranking: de 1 a 1.000")
	}
	if p.Ranking.MinRoom < 1 || p.Ranking.MinRoom > 1000 {
		bad("tamanho mínimo da sala no ranking: de 1 a 1.000")
	}
	if len(p.Ranking.FloorNames) > 500 {
		bad("nomes de andar demais (até 500)")
	}
	for spec, name := range p.Ranking.FloorNames {
		if spec == "" || len(spec) > 64 || strings.IndexFunc(spec, unicode.IsControl) >= 0 {
			bad("faixa inválida no nome de andar")
			continue
		}
		if err := text(name, 40); err != nil {
			bad("nome do andar da faixa %s: %v", spec, err)
		}
	}
	n := p.Notify
	if n.MinHours < 1 || n.MinHours > 168 || n.WorkStart < 0 || n.WorkEnd > 24 || n.WorkStart >= n.WorkEnd {
		bad("avisos: intervalo de 1 a 168 horas e expediente com início antes do fim")
	}
	return errors.Join(errs...)
}

// text: texto de exibição, sem caracteres de controle, de 1 a max caracteres.
func text(s string, max int) error {
	n := len([]rune(s))
	if strings.TrimSpace(s) == "" || n > max {
		return fmt.Errorf("de 1 a %d caracteres", max)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errors.New("sem caracteres de controle")
	}
	return nil
}

// Parse lê a política gravada. Campos que faltam (de uma versão anterior do patchd) ficam com
// o padrão; a política lida é validada.
func Parse(b []byte) (Policy, error) {
	p := Default()
	if err := json.Unmarshal(b, &p); err != nil {
		return Default(), err
	}
	def := Default()
	// O nome padrão mudou: a política gravada com o padrão antigo passa a mostrar o novo (um
	// nome escolhido pelo admin fica como está).
	if p.Brand.Name == OldDefaultName {
		p.Brand.Name = def.Brand.Name
	}
	for c, cp := range def.Criteria {
		if _, ok := p.Criteria[c]; !ok {
			if p.Criteria == nil {
				p.Criteria = map[Criterion]CriterionPolicy{}
			}
			p.Criteria[c] = cp
		}
	}
	for s, v := range def.Credits {
		if _, ok := p.Credits[s]; !ok {
			if p.Credits == nil {
				p.Credits = map[Status]float64{}
			}
			p.Credits[s] = v
		}
	}
	return p, p.Validate()
}

// InScope diz se a máquina do grupo ring participa (a chave geral e o piloto).
func (p *Policy) InScope(ring int) bool {
	return p.Enabled && (len(p.PilotRings) == 0 || slices.Contains(p.PilotRings, ring))
}

// Location é o fuso da política (fallback: o do painel, ou UTC).
func (p *Policy) Location(fallback *time.Location) *time.Location {
	if p.Timezone != "" {
		if l, err := time.LoadLocation(p.Timezone); err == nil {
			return l
		}
	}
	if fallback != nil {
		return fallback
	}
	return time.UTC
}

// Eligible diz se o dia conta na sequência (dia da semana escolhido e não feriado).
func (p *Policy) Eligible(day time.Time) bool {
	return slices.Contains(p.Workdays, int(day.Weekday())) && !slices.Contains(p.Holidays, day.Format(time.DateOnly))
}

// LevelFor devolve o nível dos dias protegidos (índice -1: nenhum ainda) e o próximo.
func (p *Policy) LevelFor(days int) (cur int, next *Level) {
	cur = -1
	for i, l := range p.Levels {
		if days >= l.MinDays {
			cur = i
		}
	}
	if cur+1 < len(p.Levels) {
		next = &p.Levels[cur+1]
	}
	return cur, next
}
