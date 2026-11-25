package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
)

// Rankings do Shield por lugar (docs/shield.md): prédio e sala do patrimônio (o tombo no nome
// da máquina) e o andar, que é a faixa de IP dos grupos de implantação (Automação › Grupos ›
// Regras) nos prédios com máquinas em mais de uma faixa. A máquina fica na faixa mais
// específica (a menor) que contém algum IP dela, como na regra do grupo.

// shieldPlaces acha o lugar de cada máquina (uma leitura do parque, do patrimônio e das faixas).
func (a *api) shieldPlaces(ctx context.Context, in *shieldInputs) error {
	in.places, in.floorStart = map[string]shield.Place{}, map[string]uint32{}
	all, err := a.fleetEquip(ctx)
	if err != nil {
		return err
	}
	rr, err := a.loadRingRules(ctx)
	if err != nil {
		return err
	}
	for _, r := range rr.ips {
		in.floorStart[r.Spec] = addr4(r.rng.lo)
	}
	for _, e := range all {
		p := shield.Place{Building: e.Asset.Building, Room: e.Asset.Room}
		var best *ipRule
		for i := range rr.ips {
			r := &rr.ips[i]
			if !e.inRange(r.rng) {
				continue
			}
			if best == nil || r.size < best.size || (r.size == best.size && r.Spec < best.Spec) {
				best = r
			}
		}
		if best != nil {
			p.Floor = best.Spec
		}
		if p != (shield.Place{}) {
			in.places[e.ID] = p
		}
	}
	return nil
}

// shieldLabels dá o nome que as pessoas veem: o prédio, a sala, e o andar com o nome da
// política ou "Faixa N" (N pela ordem do começo da faixa dentro do prédio) — nunca o IP.
func shieldLabels(pol *shield.Policy, ranks []shield.Rank, floorStart map[string]uint32) []store.ShieldRank {
	floors := map[string][]string{}
	for _, r := range ranks {
		if r.Kind == shield.RankFloor {
			floors[r.Building] = append(floors[r.Building], r.Name)
		}
	}
	for b := range floors {
		slices.SortFunc(floors[b], func(x, y string) int {
			if c := cmp.Compare(floorStart[x], floorStart[y]); c != 0 {
				return c
			}
			return cmp.Compare(x, y)
		})
	}
	out := make([]store.ShieldRank, 0, len(ranks))
	for _, r := range ranks {
		x := store.ShieldRank{Rank: r}
		switch r.Kind {
		case shield.RankBuilding:
			x.Label = r.Building
		case shield.RankRoom:
			x.Label = r.Name
		case shield.RankFloor:
			if n := pol.Ranking.FloorNames[r.Name]; n != "" {
				x.Label = n
			} else {
				x.Label = fmt.Sprintf("Faixa %d", slices.Index(floors[r.Building], r.Name)+1)
			}
		}
		out = append(out, x)
	}
	return out
}

// shieldSaveRankings monta e grava os rankings por lugar, só quando mudaram.
func (a *api) shieldSaveRankings(ctx context.Context, db shieldStore, pol *shield.Policy, in shieldInputs, placed []shield.PlaceScore) error {
	list := shieldLabels(pol, shield.PlaceRankings(pol, placed), in.floorStart)
	b, _ := json.Marshal(list)
	sum := sha256.Sum256(b)
	key := hex.EncodeToString(sum[:])
	a.shield.mu.Lock()
	same := a.shield.rankSum == key
	a.shield.mu.Unlock()
	if same {
		return nil
	}
	if err := db.ReplaceShieldRankings(ctx, list); err != nil {
		return err
	}
	a.shield.mu.Lock()
	a.shield.rankSum = key
	a.shield.mu.Unlock()
	return nil
}
