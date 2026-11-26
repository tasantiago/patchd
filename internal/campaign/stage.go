package campaign

import (
	"context"
	"slices"

	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/window"
)

// stageCtx é a etapa atual de uma campanha por localização, lida uma vez por lançamento ou
// rodada de novas tentativas (sem uma ida ao banco por máquina): os alvos gravados, as janelas
// dos prédios e onde as máquinas estão agora. nil na campanha legada: tudo passa.
type stageCtx struct {
	targets map[string]Target
	ids     []string // os alvos, em ordem
	windows map[int64]window.Window
	locs    map[string]Location // nil: sem conferência da localização atual
	// floorBound: a etapa é o piloto por andar; a máquina só vale no andar gravado.
	floorBound bool
}

// Motivos de quem fica fora por ter saído do escopo.
const (
	SkipMovedBuilding = "mudou de prédio depois da criação da campanha"
	SkipMovedFloor    = "mudou de andar depois da criação da campanha"
	SkipNotTarget     = "fora do escopo da campanha"
)

func (e *Engine) loadStage(ctx context.Context, c *Campaign) (*stageCtx, error) {
	list, err := e.Store.CampaignTargets(ctx, c.ID, c.CurrentIdx)
	if err != nil {
		return nil, err
	}
	sc := &stageCtx{targets: make(map[string]Target, len(list)), windows: map[int64]window.Window{}}
	for _, t := range list {
		sc.targets[t.MachineID] = t
		sc.ids = append(sc.ids, t.MachineID)
	}
	slices.Sort(sc.ids)
	sc.floorBound = c.PilotStage(c.CurrentIdx) && c.Scope.Pilot != nil && c.Scope.Pilot.Kind == PilotFloor
	if c.UseWindow && e.BuildingWindows != nil {
		if sc.windows, err = e.BuildingWindows(ctx); err != nil {
			return nil, err
		}
	}
	if e.Locate != nil && len(sc.ids) > 0 {
		if sc.locs, err = e.Locate(ctx, sc.ids); err != nil {
			return nil, err
		}
	}
	return sc, nil
}

// gate diz por que a máquina não recebe job nesta etapa ("" passa): não é alvo dela, ou saiu
// do lugar em que foi escolhida. Sem localização na criação (incluída explicitamente), passa.
func (sc *stageCtx) gate(id string) string {
	if sc == nil {
		return ""
	}
	t, ok := sc.targets[id]
	if !ok {
		return SkipNotTarget
	}
	if sc.locs == nil || t.Via == ViaUnlocated {
		return ""
	}
	l, found := sc.locs[id]
	if !found {
		return "" // fora do parque agora (aposentada, excluída): o jobs.Issue responde
	}
	if l.BuildingID != t.BuildingID {
		return SkipMovedBuilding
	}
	if (t.Via == ViaFloor || sc.floorBound) && (l.Conflict || l.FloorKey != t.FloorKey) {
		return SkipMovedFloor
	}
	return ""
}

// window: a janela do prédio gravado da máquina.
func (sc *stageCtx) window(id string) (window.Window, bool) {
	t, ok := sc.targets[id]
	if !ok || t.BuildingID == 0 {
		return window.Window{}, false
	}
	w, ok := sc.windows[t.BuildingID]
	return w, ok
}

func (sc *stageCtx) buildingName(id string) string {
	if t := sc.targets[id]; t.Building != "" {
		return texto.PlaceName(t.Building)
	}
	return "sem localização"
}

// missingWindows: os prédios da etapa sem janela ativa (nomes, em ordem).
func (sc *stageCtx) missingWindows() []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range sc.ids {
		if _, ok := sc.window(id); ok {
			continue
		}
		n := sc.buildingName(id)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}
