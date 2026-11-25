package api

import (
	"testing"

	"github.com/tasantiago/patchd/internal/shield"
)

func TestNamedFloorRanking(t *testing.T) {
	p := shield.Default()
	p.Ranking.Floors = true
	p.Ranking.MinGroup = 1
	// Duas faixas do mesmo andar chegam ao ranking com a mesma chave física.
	list := []shield.PlaceScore{
		{Place: shield.Place{Building: "Sede", Floor: namedFloorPrefix + "Térreo"}, Sealed: true},
		{Place: shield.Place{Building: "Sede", Floor: namedFloorPrefix + "Térreo"}},
		{Place: shield.Place{Building: "Sede", Floor: namedFloorPrefix + "1º andar"}, Sealed: true},
		{Place: shield.Place{Building: "Anexo", Floor: namedFloorPrefix + "Térreo"}},
		{Place: shield.Place{Building: "Anexo", Floor: namedFloorPrefix + "1º andar"}},
	}
	ranks := shieldLabels(&p, shield.PlaceRankings(&p, list), nil)
	found := false
	for _, r := range ranks {
		if r.Kind == shield.RankFloor && r.Building == "Sede" && r.Label == "Térreo" {
			found = true
			if r.Machines != 2 || r.Sealed != 1 || r.Percent() != 50 {
				t.Fatalf("agrupamento incorreto: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("andar agrupado não encontrado")
	}
}
