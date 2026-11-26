package campaign

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/texto"
)

// Campanhas por localização (docs/implantacao-por-predio.md). A campanha nova não passa
// pelos grupos de implantação numéricos: o admin escolhe prédios inteiros e andares (do
// patrimônio e das regras de Configuração › Localizações), e opcionalmente um piloto (um
// prédio ou um andar do escopo). Na confirmação, o servidor resolve as máquinas e grava cada
// uma com a etapa dela (Target): o motor não reconsulta a localização para decidir quem entra,
// só para não executar onde a máquina deixou de estar (mudou de prédio ou de andar).
//
// Etapas: sem piloto, uma (todos os alvos, em paralelo, cada um na janela do seu prédio);
// com piloto, duas (o piloto; depois da conclusão e da observação, o resto). Uma máquina está
// numa etapa só (a chave do alvo é campanha + máquina).

// Como o alvo entrou no escopo.
const (
	ViaBuilding  = "predio"    // prédio inteiro (com ou sem andar identificado)
	ViaFloor     = "andar"     // um andar escolhido do prédio
	ViaUnlocated = "sem_local" // sem localização identificada, incluída explicitamente
)

// Tipos de piloto.
const (
	PilotBuilding = "predio"
	PilotFloor    = "andar"
)

// MaxStages: o máximo de etapas de uma campanha por localização.
const MaxStages = 20

// ScopeBuilding é um prédio do escopo (ID estável e o nome do patrimônio na criação).
type ScopeBuilding struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ScopeFloor é um andar do escopo: o prédio, a chave do andar (a mesma do resolvedor) e o
// nome que a tela mostrou.
type ScopeFloor struct {
	BuildingID int64  `json:"building_id"`
	Building   string `json:"building"`
	Key        string `json:"key"`
	Label      string `json:"label"`
}

// Pilot é o piloto escolhido para a campanha.
type Pilot struct {
	Kind       string `json:"kind"` // PilotBuilding ou PilotFloor
	BuildingID int64  `json:"building_id"`
	Building   string `json:"building"`
	FloorKey   string `json:"floor_key,omitempty"`
	FloorLabel string `json:"floor_label,omitempty"`
}

// Stage é uma etapa da campanha.
type Stage struct {
	Label    string `json:"label"`
	Pilot    bool   `json:"pilot,omitempty"`
	Machines int    `json:"machines"`
}

// Scope é o escopo persistido da campanha por localização (nil: campanha legada, por grupos
// de implantação).
type Scope struct {
	Buildings []ScopeBuilding `json:"buildings,omitempty"`
	Floors    []ScopeFloor    `json:"floors,omitempty"`
	Unlocated bool            `json:"unlocated,omitempty"`
	Pilot     *Pilot          `json:"pilot,omitempty"`
	Stages    []Stage         `json:"stages"`
	// Excluded: as máquinas dos prédios e andares escolhidos que ficaram fora na criação, por
	// motivo (andar em conflito, por exemplo).
	Excluded map[string]int `json:"excluded,omitempty"`
}

// Target é uma máquina da campanha por localização, gravada na criação.
type Target struct {
	MachineID  string
	Stage      int
	BuildingID int64  // 0: sem localização
	Building   string // o nome do prédio na criação (histórico)
	FloorKey   string // a chave do andar na criação ("" sem andar)
	Floor      string // o nome do andar na criação
	Via        string
}

// Location é onde o resolvedor põe a máquina agora.
type Location struct {
	BuildingID int64
	Building   string
	FloorKey   string
	Floor      string
	Conflict   bool // regras de andares diferentes casam: nenhum andar
}

// Candidate é uma máquina do parque com a localização atual, para o planejamento.
type Candidate struct {
	MachineID string
	Loc       Location
}

// Selection é o que o admin escolheu (já conferido contra os prédios e andares que existem).
type Selection struct {
	Buildings []ScopeBuilding
	Floors    []ScopeFloor
	Unlocated bool
	Pilot     *Pilot
}

// Motivos de exclusão na criação.
const (
	ExcludedConflict = "andar em conflito (fora do andar escolhido)"
)

// Erros do planejamento.
var (
	ErrEmptyScope      = errors.New("nenhum equipamento no escopo escolhido")
	ErrPilotOutside    = errors.New("o piloto precisa ser um prédio ou um andar do escopo")
	ErrPilotEmpty      = errors.New("o piloto não tem equipamentos elegíveis")
	ErrPilotWholeScope = errors.New("o piloto cobre todo o escopo: escolha sem piloto ou amplie o escopo")
)

// PlanScope resolve o escopo: os alvos (sem repetição, um por máquina) e as etapas. Uma
// máquina de um prédio inteiro escolhido entra mesmo sem andar identificado ou em conflito;
// pelo andar, só a identificada nele, sem conflito. O piloto por andar nunca leva máquina em
// conflito nem de outro andar.
func PlanScope(sel Selection, fleet []Candidate) (Scope, []Target, error) {
	sc := Scope{Buildings: slices.Clone(sel.Buildings), Floors: slices.Clone(sel.Floors), Unlocated: sel.Unlocated, Excluded: map[string]int{}}
	whole := map[int64]bool{}
	for _, b := range sel.Buildings {
		whole[b.ID] = true
	}
	type fk struct {
		b int64
		k string
	}
	floors := map[fk]ScopeFloor{}
	for _, f := range sel.Floors {
		floors[fk{f.BuildingID, f.Key}] = f
	}
	if p := sel.Pilot; p != nil {
		inScope := whole[p.BuildingID]
		switch p.Kind {
		case PilotBuilding:
			if !inScope {
				for _, f := range sel.Floors {
					inScope = inScope || f.BuildingID == p.BuildingID
				}
			}
		case PilotFloor:
			_, isFloor := floors[fk{p.BuildingID, p.FloorKey}]
			inScope = (inScope || isFloor) && p.FloorKey != ""
		default:
			inScope = false
		}
		if !inScope {
			return sc, nil, ErrPilotOutside
		}
	}
	var out []Target
	seen := map[string]bool{}
	for _, m := range fleet {
		if seen[m.MachineID] || m.MachineID == "" {
			continue
		}
		seen[m.MachineID] = true // a primeira ocorrência vale, entre ou não no escopo
		l := m.Loc
		t := Target{MachineID: m.MachineID, BuildingID: l.BuildingID, Building: l.Building, FloorKey: l.FloorKey, Floor: l.Floor}
		if l.Conflict {
			t.FloorKey, t.Floor = "", ""
		}
		_, onFloor := floors[fk{l.BuildingID, l.FloorKey}]
		switch {
		case l.BuildingID == 0:
			if !sel.Unlocated {
				continue
			}
			t.Via, t.Building, t.FloorKey, t.Floor = ViaUnlocated, "", "", ""
		case whole[l.BuildingID]:
			t.Via = ViaBuilding
		case onFloor && l.FloorKey != "" && !l.Conflict:
			t.Via = ViaFloor
		default:
			if l.Conflict && hasFloorOf(sel.Floors, l.BuildingID) {
				sc.Excluded[ExcludedConflict]++
			}
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return sc, nil, ErrEmptyScope
	}
	slices.SortFunc(out, func(a, b Target) int { return cmp.Compare(a.MachineID, b.MachineID) })
	if p := sel.Pilot; p != nil {
		n := 0
		for i := range out {
			t := &out[i]
			if t.BuildingID != p.BuildingID || t.Via == ViaUnlocated {
				t.Stage = 1
				continue
			}
			if p.Kind == PilotFloor && (t.FloorKey != p.FloorKey || t.FloorKey == "") {
				t.Stage = 1
				continue
			}
			n++
		}
		if n == 0 {
			return sc, nil, ErrPilotEmpty
		}
		if n == len(out) {
			return sc, nil, ErrPilotWholeScope
		}
		pc := *p
		sc.Pilot = &pc
		sc.Stages = []Stage{{Label: PilotLabel(pc), Pilot: true, Machines: n}, {Label: restLabel(sel, true), Machines: len(out) - n}}
	} else {
		sc.Stages = []Stage{{Label: restLabel(sel, false), Machines: len(out)}}
	}
	if len(sc.Excluded) == 0 {
		sc.Excluded = nil
	}
	return sc, out, nil
}

func hasFloorOf(list []ScopeFloor, b int64) bool {
	return slices.ContainsFunc(list, func(f ScopeFloor) bool { return f.BuildingID == b })
}

// PilotLabel: "Piloto — Prédio Sede, 1º andar".
func PilotLabel(p Pilot) string {
	s := "Piloto — " + texto.PlaceName(p.Building)
	if p.Kind == PilotFloor {
		s += ", " + p.FloorLabel
	}
	return s
}

// restLabel: a etapa geral.
func restLabel(sel Selection, afterPilot bool) string {
	n := len(sel.Buildings)
	ids := map[int64]bool{}
	for _, b := range sel.Buildings {
		ids[b.ID] = true
	}
	for _, f := range sel.Floors {
		if !ids[f.BuildingID] {
			ids[f.BuildingID] = true
			n++
		}
	}
	if n == 0 && sel.Unlocated {
		return "Equipamentos sem localização identificada"
	}
	var s string
	switch {
	case afterPilot:
		s = "Demais equipamentos do escopo"
	case n == 1 && len(sel.Buildings) == 1 && len(sel.Floors) == 0:
		s = texto.PlaceName(sel.Buildings[0].Name)
	default:
		s = "Prédios selecionados"
	}
	parts := []string{fmt.Sprintf("%d prédio(s)", n)}
	if sel.Unlocated {
		parts = append(parts, "sem localização")
	}
	return s + " (" + strings.Join(parts, ", ") + ")"
}

// Located: a campanha é por localização (escopo persistido), não pelos grupos numéricos.
func (c Campaign) Located() bool { return c.Scope != nil }

// StageCount: quantas etapas (grupos, na campanha legada).
func (c Campaign) StageCount() int {
	if c.Scope != nil {
		return len(c.Scope.Stages)
	}
	return len(c.Rings)
}

// StageLabel: o nome da etapa idx, como as notas e a tela mostram. Na legada, "grupo de
// implantação N".
func (c Campaign) StageLabel(idx int) string {
	if c.Scope != nil {
		if idx >= 0 && idx < len(c.Scope.Stages) {
			return c.Scope.Stages[idx].Label
		}
		return fmt.Sprintf("etapa %d", idx+1)
	}
	if idx >= 0 && idx < len(c.Rings) {
		return fmt.Sprintf("grupo de implantação %d", c.Rings[idx])
	}
	return fmt.Sprintf("grupo de implantação na posição %d", idx)
}

// PilotStage: a etapa idx é o piloto de uma campanha por localização.
func (c Campaign) PilotStage(idx int) bool {
	return c.Scope != nil && idx >= 0 && idx < len(c.Scope.Stages) && c.Scope.Stages[idx].Pilot
}

// StagesText: "Piloto — Prédio, 1º andar → Demais equipamentos", com a atual entre colchetes
// se mark.
func (c Campaign) StagesText(mark bool) string {
	parts := make([]string, c.StageCount())
	for i := range parts {
		parts[i] = c.StageLabel(i)
		if c.Scope == nil {
			parts[i] = fmt.Sprint(c.Rings[i])
		}
		if mark && i == c.CurrentIdx && c.State != StateDone {
			parts[i] = "[" + parts[i] + "]"
		}
	}
	return strings.Join(parts, " → ")
}

func (s *Scope) validate() error {
	switch {
	case len(s.Stages) == 0 || len(s.Stages) > MaxStages:
		return fmt.Errorf("a campanha por localização leva de 1 a %d etapas", MaxStages)
	case len(s.Buildings) == 0 && len(s.Floors) == 0 && !s.Unlocated:
		return errors.New("escolha ao menos um prédio ou andar")
	case s.Pilot != nil && (len(s.Stages) < 2 || !s.Stages[0].Pilot):
		return errors.New("com piloto, a primeira etapa é o piloto e há ao menos uma depois")
	case s.Pilot == nil && slices.ContainsFunc(s.Stages, func(st Stage) bool { return st.Pilot }):
		return errors.New("etapa piloto sem piloto escolhido")
	}
	return nil
}
