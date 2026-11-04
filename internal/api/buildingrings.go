package api

import (
	"net/http"
	"slices"

	"github.com/tasantiago/patchd/internal/patrimonio"
	"github.com/tasantiago/patchd/internal/store"
)

// Grupo de implantação pelo prédio do patrimônio, no inventário: a máquina nova (ou
// renomeada) de um prédio ligado a um grupo entra nele na hora, sem esperar a passada das
// regras (ringrules.go). O prédio é a regra mais forte depois da mão, então não precisa
// olhar as faixas.

// buildingRingFor: o grupo do prédio para uma máquina, no inventário. Um erro aqui não
// recusa o inventário: fica no log, e o próximo vínculo ou importação acerta.
func (a *api) buildingRingFor(r *http.Request, id, hostname string) {
	ctx := r.Context()
	links, err := a.store.BuildingRings(ctx)
	if err != nil || len(links) == 0 {
		if err != nil {
			a.logger.Error("grupo de implantação pelo prédio", "machine_id", id, "error", err)
		}
		return
	}
	assets, err := a.assetMap(ctx)
	if err != nil {
		a.logger.Error("grupo de implantação pelo prédio", "machine_id", id, "error", err)
		return
	}
	loc, ok := patrimonio.Match(hostname, assets)
	if !ok {
		return
	}
	i := slices.IndexFunc(links, func(l store.BuildingRing) bool { return l.Building == loc.Building })
	if i < 0 {
		return
	}
	n, err := a.store.ApplyRings(ctx, map[string]int{id: links[i].Ring})
	if err != nil {
		a.logger.Error("grupo de implantação pelo prédio", "machine_id", id, "error", err)
		return
	}
	if n > 0 {
		a.logger.Info("máquina no grupo de implantação do prédio", "machine_id", id, "predio", loc.Building, "ring", links[i].Ring,
			"request_id", RequestID(ctx))
	}
}
