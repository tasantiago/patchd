package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/patrimonio"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Grupo de implantação pelo prédio do patrimônio. O admin liga um prédio (o DEPREDIO do CSV
// de Configuração › Patrimônio) a um grupo; as máquinas do prédio vão para ele:
//
//   - ao gravar o vínculo e a cada importação do CSV, a frota inteira;
//   - quando chega o inventário de uma máquina nova ou renomeada, só ela.
//
// Quem foi movido à mão (detalhe da máquina, Parque, Inventário, patchd-server ring) fica
// como exceção: o vínculo não o tira do grupo em que está, até alguém devolvê-lo ao vínculo.
// Remover o vínculo não move ninguém.

// buildingOf: o prédio de cada máquina da frota, pelo nome (vazio: sem tombo no patrimônio).
func buildingOf(machines []protocol.MachineSummary, assets map[string]store.AssetLocation) map[string]string {
	out := make(map[string]string, len(machines))
	for _, m := range machines {
		if loc, ok := patrimonio.Match(m.Hostname, assets); ok && loc.Building != "" {
			out[m.ID] = loc.Building
		}
	}
	return out
}

// applyBuildingRings põe as máquinas (não movidas à mão) no grupo do prédio delas.
func (a *api) applyBuildingRings(ctx context.Context) (int, error) {
	links, err := a.store.BuildingRings(ctx)
	if err != nil || len(links) == 0 {
		return 0, err
	}
	ring := map[string]int{}
	for _, l := range links {
		ring[l.Building] = l.Ring
	}
	machines, err := a.store.Machines(ctx)
	if err != nil {
		return 0, err
	}
	assets, err := a.assetMap(ctx)
	if err != nil {
		return 0, err
	}
	target := map[string]int{}
	for id, b := range buildingOf(machines, assets) {
		if r, ok := ring[b]; ok {
			target[id] = r
		}
	}
	return a.store.ApplyRings(ctx, target)
}

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

// --- Tela ------------------------------------------------------------------------------

type buildingRingRow struct {
	Building, Label, By, Updated string
	Ring                         int
	Machines, InRing, Pinned     int
	Missing                      bool // o prédio não está no patrimônio atual
}

type buildingOption struct {
	Name     string
	Machines int
	Ring     int // -1: sem vínculo
}

// buildingRings monta o cartão "Grupo de implantação por prédio".
func (a *api) buildingRings(ctx context.Context, v *ringsView, machines []protocol.MachineSummary) error {
	assets, err := a.assetMap(ctx)
	if err != nil {
		return err
	}
	v.HasAssets = len(assets) > 0
	links, err := a.store.BuildingRings(ctx)
	if err != nil {
		return err
	}
	pins, err := a.store.RingPins(ctx)
	if err != nil {
		return err
	}
	inAssets := map[string]bool{}
	for _, l := range assets {
		if l.Building != "" {
			inAssets[l.Building] = true
		}
	}
	ringOf := map[string]int{}
	for _, m := range machines {
		ringOf[m.ID] = m.Ring
	}
	perBuilding := map[string][]string{}
	for id, b := range buildingOf(machines, assets) {
		perBuilding[b] = append(perBuilding[b], id)
	}
	linked := map[string]int{}
	now := a.now()
	for _, l := range links {
		linked[l.Building] = l.Ring
		row := buildingRingRow{Building: l.Building, Ring: l.Ring, Label: ringLabel(l.Ring), By: l.UpdatedBy,
			Updated: ago(now.Sub(l.UpdatedAt)), Missing: !inAssets[l.Building]}
		for _, id := range perBuilding[l.Building] {
			row.Machines++
			switch {
			case ringOf[id] == l.Ring:
				row.InRing++
			case pins[id]:
				row.Pinned++
			}
		}
		v.Buildings = append(v.Buildings, row)
	}
	for b := range inAssets {
		r, ok := linked[b]
		if !ok {
			r = -1
		}
		v.BuildingOpts = append(v.BuildingOpts, buildingOption{Name: b, Machines: len(perBuilding[b]), Ring: r})
	}
	// Os prédios com máquina primeiro, pelo nome.
	slices.SortFunc(v.BuildingOpts, func(x, y buildingOption) int {
		return cmp.Or(cmp.Compare(min(y.Machines, 1), min(x.Machines, 1)), cmp.Compare(x.Name, y.Name))
	})
	return nil
}

// formBuilding: o prédio do formulário, como está no patrimônio atual.
func (a *api) formBuilding(r *http.Request) (string, bool, error) {
	b := strings.TrimSpace(r.PostFormValue("predio"))
	if b == "" || len(b) > 4*store.MaxBuildingLen {
		return "", false, nil
	}
	assets, err := a.assetMap(r.Context())
	if err != nil {
		return "", false, err
	}
	for _, l := range assets {
		if l.Building == b {
			return b, true, nil
		}
	}
	return "", false, nil
}

// buildingMachines: as máquinas da frota no prédio.
func (a *api) buildingMachines(ctx context.Context, building string) ([]string, error) {
	machines, err := a.store.Machines(ctx)
	if err != nil {
		return nil, err
	}
	assets, err := a.assetMap(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id, b := range buildingOf(machines, assets) {
		if b == building {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func ringsBack(w http.ResponseWriter, r *http.Request, q url.Values) {
	http.Redirect(w, r, "/painel/grupos-implantacao?"+q.Encode()+"#predios", http.StatusSeeOther)
}

// setBuildingRingAction grava o vínculo e aplica na frota; com excecoes=1, as movidas à mão
// do prédio voltam ao vínculo antes.
func (a *api) setBuildingRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	ring, ok := formRing(r, "anel")
	if !ok {
		ringsBack(w, r, url.Values{"erro": {"anel"}})
		return
	}
	b, ok, err := a.formBuilding(r)
	if err != nil {
		a.internalError(w, r, "ler o patrimônio", err)
		return
	}
	if !ok {
		ringsBack(w, r, url.Values{"erro": {"predio"}})
		return
	}
	if err := a.store.SetBuildingRing(r.Context(), store.BuildingRing{Building: b, Ring: ring, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "gravar o vínculo do prédio", err)
		return
	}
	unpinned := 0
	if r.PostFormValue("excecoes") == "1" {
		ids, err := a.buildingMachines(r.Context(), b)
		if err == nil {
			unpinned, err = a.store.UnpinRings(r.Context(), ids)
		}
		if err != nil {
			a.internalError(w, r, "devolver as exceções ao vínculo", err)
			return
		}
	}
	moved, err := a.applyBuildingRings(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar os vínculos de prédio", err)
		return
	}
	a.logger.Info("prédio ligado a grupo de implantação", "predio", b, "ring", ring, "movidas", moved, "devolvidas", unpinned,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo", b, fmt.Sprintf("grupo de implantação %d · %d máquina(s) movida(s) · %d exceção(ões) devolvida(s)", ring, moved, unpinned))
	ringsBack(w, r, url.Values{"vinculo": {"1"}, "anel": {strconv.Itoa(ring)}, "movidas": {strconv.Itoa(moved)}})
}

// deleteBuildingRingAction remove o vínculo; ninguém muda de grupo.
func (a *api) deleteBuildingRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	b := strings.TrimSpace(r.PostFormValue("predio"))
	if b == "" || len(b) > 4*store.MaxBuildingLen {
		ringsBack(w, r, url.Values{"erro": {"predio"}})
		return
	}
	removed, err := a.store.DeleteBuildingRing(r.Context(), b)
	if err != nil {
		a.internalError(w, r, "remover o vínculo do prédio", err)
		return
	}
	if !removed {
		ringsBack(w, r, url.Values{"erro": {"sem-vinculo"}})
		return
	}
	a.logger.Info("vínculo de prédio removido", "predio", b, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo_remover", b, "")
	ringsBack(w, r, url.Values{"vinculo-removido": {"1"}})
}

// unpinBuildingAction devolve ao vínculo as máquinas do prédio movidas à mão.
func (a *api) unpinBuildingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	b := strings.TrimSpace(r.PostFormValue("predio"))
	links, err := a.store.BuildingRings(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os vínculos de prédio", err)
		return
	}
	if !slices.ContainsFunc(links, func(l store.BuildingRing) bool { return l.Building == b }) {
		ringsBack(w, r, url.Values{"erro": {"sem-vinculo"}})
		return
	}
	ids, err := a.buildingMachines(r.Context(), b)
	if err != nil {
		a.internalError(w, r, "listar as máquinas do prédio", err)
		return
	}
	unpinned, err := a.store.UnpinRings(r.Context(), ids)
	if err != nil {
		a.internalError(w, r, "devolver as exceções ao vínculo", err)
		return
	}
	moved, err := a.applyBuildingRings(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar os vínculos de prédio", err)
		return
	}
	a.logger.Info("exceções do prédio devolvidas ao vínculo", "predio", b, "devolvidas", unpinned, "movidas", moved,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo_excecoes", b, fmt.Sprintf("%d exceção(ões) devolvida(s) · %d máquina(s) movida(s)", unpinned, moved))
	ringsBack(w, r, url.Values{"devolvidas": {strconv.Itoa(unpinned)}, "movidas": {strconv.Itoa(moved)}})
}
