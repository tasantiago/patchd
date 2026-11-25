package shield

import (
	"cmp"
	"slices"
)

// Rankings por lugar: prédio, andar e sala, para a disputa entre as pessoas de cada lugar.
// O prédio e a sala vêm do patrimônio; o andar é a faixa de IP (quem calcula a faixa de cada
// máquina é o servidor), e só existe nos prédios com máquinas em mais de uma faixa — num
// prédio de uma faixa só, "andar" e "prédio" seriam a mesma coisa. Como no ranking dos grupos,
// as máquinas sem dado contam no denominador, e lugar pequeno demais não aparece (privacidade).

// Tipos de ranking por lugar.
const (
	RankBuilding = "predio"
	RankFloor    = "andar"
	RankRoom     = "sala"
)

// Place é onde a máquina está. Floor é a chave da faixa de IP ("" sem faixa).
type Place struct {
	Building, Room, Floor string
}

// PlaceScore é uma máquina do Shield com o lugar dela.
type PlaceScore struct {
	Place
	Sealed bool
	Score  *float64 // nil: sem dado (conta como zero)
}

// Rank é um lugar no ranking.
type Rank struct {
	Kind     string // predio, andar ou sala
	Key      string // o prédio; prédio + faixa; prédio + sala
	Building string // o prédio (no prédio, ele mesmo)
	Name     string // a sala ou a faixa (no prédio, vazio)
	Machines int
	Sealed   int
	Average  float64 // média da pontuação (sem dado vale 0), duas casas
	Pos      int     // a posição entre os do mesmo tipo (andares e salas: dentro do prédio)
	Overall  int     // a posição entre todos do mesmo tipo (andares e salas de todos os prédios)
	Of       int     // quantos do mesmo tipo dentro do prédio (andares e salas); prédios: todos
}

// Percent: a porcentagem no selo (para baixo).
func (r Rank) Percent() int {
	if r.Machines == 0 {
		return 0
	}
	return r.Sealed * 100 / r.Machines
}

// PlaceKey é a chave de um lugar (o separador não aparece em texto do patrimônio).
func PlaceKey(building, name string) string {
	if name == "" {
		return building
	}
	return building + "\x1f" + name
}

// PlaceRankings monta os três rankings. Máquinas sem prédio ficam de fora de todos; sem sala,
// do das salas; sem faixa, do dos andares.
func PlaceRankings(p *Policy, list []PlaceScore) []Rank {
	type acc struct {
		r   Rank
		sum float64
	}
	groups := map[string]*acc{}
	add := func(kind, building, name string, m PlaceScore) {
		key := kind + "|" + PlaceKey(building, name)
		g := groups[key]
		if g == nil {
			g = &acc{r: Rank{Kind: kind, Key: PlaceKey(building, name), Building: building, Name: name}}
			groups[key] = g
		}
		g.r.Machines++
		if m.Sealed {
			g.r.Sealed++
		}
		if m.Score != nil {
			g.sum += *m.Score
		}
	}
	floorsOf := map[string]map[string]bool{}
	for _, m := range list {
		if m.Building == "" {
			continue
		}
		if p.Ranking.Buildings {
			add(RankBuilding, m.Building, "", m)
		}
		if p.Ranking.Rooms && m.Room != "" {
			add(RankRoom, m.Building, m.Room, m)
		}
		if m.Floor != "" {
			if floorsOf[m.Building] == nil {
				floorsOf[m.Building] = map[string]bool{}
			}
			floorsOf[m.Building][m.Floor] = true
		}
	}
	if p.Ranking.Floors {
		for _, m := range list {
			if m.Building != "" && m.Floor != "" && len(floorsOf[m.Building]) > 1 {
				add(RankFloor, m.Building, m.Floor, m)
			}
		}
	}
	var out []Rank
	for _, g := range groups {
		least := p.Ranking.MinGroup
		if g.r.Kind == RankRoom {
			least = p.Ranking.MinRoom
		}
		if g.r.Machines < least {
			continue
		}
		g.r.Average = float64(int(g.sum/float64(g.r.Machines)*100)) / 100
		out = append(out, g.r)
	}
	better := func(a, b Rank) int {
		// Mais máquinas no selo (proporção), depois a média, depois o nome (estável).
		if c := cmp.Compare(b.Sealed*a.Machines, a.Sealed*b.Machines); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Average, a.Average); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	}
	slices.SortFunc(out, func(a, b Rank) int {
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return better(a, b)
	})
	overall := map[string]int{}
	inside := map[string]int{}
	for i := range out {
		r := &out[i]
		overall[r.Kind]++
		r.Overall = overall[r.Kind]
		if r.Kind == RankBuilding {
			r.Pos = r.Overall
			continue
		}
		k := r.Kind + "|" + r.Building
		inside[k]++
		r.Pos = inside[k]
	}
	for i := range out {
		r := &out[i]
		if r.Kind == RankBuilding {
			r.Of = overall[RankBuilding]
		} else {
			r.Of = inside[r.Kind+"|"+r.Building]
		}
	}
	return out
}
