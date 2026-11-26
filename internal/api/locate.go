package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

// O resolvedor de localização física: um só para o Shield (rankings de prédio e andar),
// Configuração › Localizações, Automação › Prédios e implantação, as campanhas por prédio e o
// motor (LocateFleet). O prédio vem do patrimônio (o tombo no nome da máquina) e o ID dele da
// tabela de prédios; o andar, das expressões regulares do prédio (Go/RE2, só no IPv4 principal)
// e, quando nenhuma casa, da faixa de IP mais específica dos grupos legados com o nome cadastrado
// para aquele prédio. Duas regras de nomes diferentes que casam o mesmo IP são conflito: a
// máquina fica sem andar (o prédio vale; o andar, não).

// locator: as regras compiladas uma vez por leitura (sem compilar regex por máquina).
type locator struct {
	ids    map[string]int64 // prédio (nome exato) → ID
	names  map[store.FloorKey]string
	regex  map[string][]floorRegexRule
	ranges []ipRule
}

// machineLoc: onde a máquina está.
type machineLoc struct {
	BuildingID     int64
	Building, Room string
	// FloorKey: "andar:<nome>" (expressão ou faixa com nome) ou a faixa sem nome; vazio: sem
	// andar. Floor: o nome que a tela mostra.
	FloorKey, Floor string
	Conflict        bool
}

func (a *api) loadLocator(ctx context.Context) (*locator, error) {
	names, err := a.floorNames(ctx)
	if err != nil {
		return nil, err
	}
	rr, err := a.loadRingRules(ctx)
	if err != nil {
		return nil, err
	}
	bs, err := a.store.Buildings(ctx)
	if err != nil {
		return nil, err
	}
	l := &locator{ids: make(map[string]int64, len(bs)), names: names, regex: compileFloorRegex(names), ranges: rr.ips}
	for _, b := range bs {
		l.ids[b.Name] = b.ID
	}
	return l, nil
}

// rangeLabel: o andar de uma faixa sem nome, para o administrador ("faixa 10.1.2.0/24").
func rangeLabel(spec string) string { return "faixa " + spec }

func (l *locator) locate(e equip) machineLoc {
	m := machineLoc{Building: e.Asset.Building, Room: e.Asset.Room}
	if m.Building != "" {
		m.BuildingID = l.ids[m.Building]
	}
	if matches := matchingFloorRegex(e, l.regex[m.Building]); len(matches) > 0 {
		name, conflict := regexFloorName(matches)
		if conflict {
			m.Conflict = true
		} else {
			m.FloorKey, m.Floor = namedFloorPrefix+name, name
		}
		return m
	}
	if best := floorIP(e, l.ranges); best != nil {
		m.FloorKey, m.Floor = best.Spec, rangeLabel(best.Spec)
		if name := l.names[store.FloorKey{Building: m.Building, Spec: best.Spec}]; name != "" {
			m.FloorKey, m.Floor = namedFloorPrefix+name, name
		}
	}
	return m
}

// campaignLocation: a localização como o motor e o planejamento das campanhas a veem (sem
// prédio, sem andar).
func (m machineLoc) campaignLocation() campaign.Location {
	if m.Building == "" || m.BuildingID == 0 {
		return campaign.Location{}
	}
	return campaign.Location{BuildingID: m.BuildingID, Building: m.Building, FloorKey: m.FloorKey, Floor: m.Floor, Conflict: m.Conflict}
}

// fleetLocations: o parque com a localização de cada máquina.
type locatedEquip struct {
	equip
	Loc machineLoc
}

func (a *api) fleetLocations(ctx context.Context) ([]locatedEquip, *locator, error) {
	fleet, err := a.fleetEquip(ctx)
	if err != nil {
		return nil, nil, err
	}
	l, err := a.loadLocator(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]locatedEquip, len(fleet))
	for i, e := range fleet {
		out[i] = locatedEquip{equip: e, Loc: l.locate(e)}
	}
	return out, l, nil
}

// LocateFleet devolve onde o resolvedor põe as máquinas ids agora, para o motor das campanhas
// por localização (cmd/server); ids nil: todas. As que não estão no parque (aposentadas,
// excluídas) ficam fora.
func LocateFleet(ctx context.Context, st Store, logger *slog.Logger, ids []string) (map[string]campaign.Location, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	list, _, err := a.fleetLocations(ctx)
	if err != nil {
		return nil, err
	}
	var want map[string]bool // nil: todas
	if ids != nil {
		want = make(map[string]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
	}
	out := make(map[string]campaign.Location, len(list))
	for _, e := range list {
		if want == nil || want[e.ID] {
			out[e.ID] = e.Loc.campaignLocation()
		}
	}
	return out, nil
}

// windowStore: o que ActiveBuildingWindows lê.
type windowStore interface {
	BuildingWindows(ctx context.Context) (map[int64]store.BuildingWindow, error)
}

// ActiveBuildingWindows: as janelas ativas por prédio (a desativada não vale para campanha).
func ActiveBuildingWindows(ctx context.Context, st windowStore) (map[int64]window.Window, error) {
	all, err := st.BuildingWindows(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]window.Window, len(all))
	for id, w := range all {
		if w.Enabled {
			out[id] = w.Window
		}
	}
	return out, nil
}
