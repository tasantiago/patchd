package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/window"
)

type contratoPrediosID interface {
	contratoPatrimonio
	Buildings(ctx context.Context) ([]Building, error)
	BuildingWindows(ctx context.Context) (map[int64]BuildingWindow, error)
	SetBuildingWindows(ctx context.Context, ids []int64, w window.Window, by string) error
	SetBuildingWindowsEnabled(ctx context.Context, ids []int64, enabled bool, by string) (int, error)
	DeleteBuildingWindows(ctx context.Context, ids []int64) (int, error)
	SaveFloorLocations(ctx context.Context, list []FloorLocation, by string) error
	FloorLocations(ctx context.Context) (map[FloorKey]string, error)
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	CreateLocatedCampaign(ctx context.Context, c campaign.Campaign, targets []campaign.Target) (int64, error)
	CampaignTargets(ctx context.Context, id int64, idx int) ([]campaign.Target, error)
	Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error)
}

func predioPorNome(t *testing.T, st contratoPrediosID) map[string]Building {
	t.Helper()
	list, err := st.Buildings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Building{}
	for _, b := range list {
		out[b.Name] = b
	}
	return out
}

func bens(building string, tombos ...string) []AssetLocation {
	var out []AssetLocation
	for _, tb := range tombos {
		out = append(out, AssetLocation{Tombo: tb, Building: building})
	}
	return out
}

// Prédios com ID estável: nada de normalização (dois textos, dois prédios), renomeação só
// quando os bens batem sem dúvida, ausente marcado sem apagar, janelas e andares seguem o ID.
func testarPrediosID(t *testing.T, st contratoPrediosID) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("America/Porto_Velho")
	imp := func(list ...[]AssetLocation) {
		t.Helper()
		var all []AssetLocation
		for _, l := range list {
			all = append(all, l...)
		}
		if err := st.ReplaceAssetLocations(ctx, all, AssetImport{By: "t", File: "p.csv"}); err != nil {
			t.Fatal(err)
		}
	}
	imp(bens("PREDIO A", "1", "2", "3", "4"), bens("PREDIO B", "5", "6"), bens("Predio b", "7"))
	p := predioPorNome(t, st)
	if len(p) != 3 || !p["PREDIO B"].Present || p["Predio b"].ID == p["PREDIO B"].ID {
		t.Fatalf("prédios distintos por texto exato: %+v", p)
	}
	a, b := p["PREDIO A"], p["PREDIO B"]
	if err := st.SaveFloorLocations(ctx, []FloorLocation{{FloorKey: FloorKey{Building: "PREDIO A", Spec: "regex:^10\\.1\\.1\\."}, Name: "1º andar"}}, "t"); err != nil {
		t.Fatal(err)
	}
	noite := window.Window{Days: window.AllDays &^ (1 << 5) &^ (1 << 6), Start: 22 * time.Hour, Duration: 6 * time.Hour, Location: loc}
	if err := st.SetBuildingWindows(ctx, []int64{a.ID, b.ID}, noite, "ana"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBuildingWindows(ctx, []int64{a.ID, 999999}, noite, "ana"); !errors.Is(err, ErrUnknownBuilding) {
		t.Fatalf("prédio desconhecido: %v", err)
	}
	if err := st.SetBuildingWindows(ctx, []int64{a.ID}, window.Window{Days: 1, Start: time.Hour, Duration: time.Minute, Location: loc}, "ana"); err == nil {
		t.Fatal("janela curta demais aceita")
	}
	ws, _ := st.BuildingWindows(ctx)
	if w := ws[a.ID]; !w.Enabled || w.Window.String() != noite.String() || w.UpdatedBy != "ana" || len(ws) != 2 {
		t.Fatalf("janelas: %+v", ws)
	}

	// Renomeação inequívoca: o A some e o mesmo conjunto de bens aparece com outro nome. O B
	// sai do patrimônio: fica ausente, com a janela.
	imp(bens("PREDIO A - SEDE", "1", "2", "3", "4"), bens("Predio b", "7"))
	p = predioPorNome(t, st)
	if r := p["PREDIO A - SEDE"]; r.ID != a.ID || r.RenamedFrom != "PREDIO A" || !r.Present {
		t.Fatalf("renomeação: %+v", p)
	}
	if _, ok := p["PREDIO A"]; ok {
		t.Fatal("o nome antigo continua")
	}
	if r := p["PREDIO B"]; r.ID != b.ID || r.Present {
		t.Fatalf("ausente: %+v", r)
	}
	ws, _ = st.BuildingWindows(ctx)
	if _, ok := ws[b.ID]; !ok || ws[a.ID].BuildingID != a.ID {
		t.Fatalf("as janelas seguem o ID: %+v", ws)
	}
	fl, _ := st.FloorLocations(ctx)
	if fl[FloorKey{Building: "PREDIO A - SEDE", Spec: "regex:^10\\.1\\.1\\."}] != "1º andar" {
		t.Fatalf("o andar segue a renomeação: %+v", fl)
	}

	// Ambíguo: os bens se dividem em dois nomes novos. Nenhuma troca; o antigo fica ausente.
	imp(bens("NOVO X", "1", "2"), bens("NOVO Y", "3", "4"), bens("Predio b", "7"))
	p = predioPorNome(t, st)
	if p["NOVO X"].ID == a.ID || p["NOVO Y"].ID == a.ID || p["PREDIO A - SEDE"].Present || p["PREDIO A - SEDE"].ID != a.ID {
		t.Fatalf("divisão não é renomeação: %+v", p)
	}
	// O B volta: mesmo ID, presente de novo.
	imp(bens("NOVO X", "1", "2"), bens("PREDIO B", "5"))
	if r := predioPorNome(t, st)["PREDIO B"]; r.ID != b.ID || !r.Present {
		t.Fatalf("volta: %+v", r)
	}

	if n, err := st.SetBuildingWindowsEnabled(ctx, []int64{b.ID, b.ID}, false, "rui"); err != nil || n != 1 {
		t.Fatalf("desativar: %d %v", n, err)
	}
	ws, _ = st.BuildingWindows(ctx)
	if ws[b.ID].Enabled || ws[b.ID].UpdatedBy != "rui" {
		t.Fatalf("desativada: %+v", ws[b.ID])
	}
	if n, err := st.DeleteBuildingWindows(ctx, []int64{a.ID, 424242}); err != nil || n != 1 {
		t.Fatalf("remover: %d %v", n, err)
	}

	// Campanha por localização: escopo e alvos numa gravação; máquina repetida recusa tudo.
	sc := &campaign.Scope{Buildings: []campaign.ScopeBuilding{{ID: a.ID, Name: "PREDIO A - SEDE"}},
		Pilot:  &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: a.ID, Building: "PREDIO A - SEDE", FloorKey: "andar:1º andar", FloorLabel: "1º andar"},
		Stages: []campaign.Stage{{Label: "Piloto", Pilot: true, Machines: 1}, {Label: "Demais", Machines: 2}}}
	c := campaign.Campaign{Name: "por prédio", Type: jobs.TypeRescan, Scope: sc, TTL: time.Hour, CreatedBy: "t"}
	if _, err := st.CreateCampaign(ctx, c); err == nil {
		t.Fatal("campanha por localização sem alvos aceita")
	}
	tg := []campaign.Target{{MachineID: "m1", Stage: 0, BuildingID: a.ID, Building: "PREDIO A - SEDE", FloorKey: "andar:1º andar", Floor: "1º andar", Via: campaign.ViaBuilding},
		{MachineID: "m2", Stage: 1, BuildingID: a.ID, Building: "PREDIO A - SEDE", Via: campaign.ViaBuilding},
		{MachineID: "m3", Stage: 1, Via: campaign.ViaUnlocated}}
	if _, err := st.CreateLocatedCampaign(ctx, c, append(tg[:2:2], campaign.Target{MachineID: "m1", Stage: 1, Via: campaign.ViaBuilding})); err == nil {
		t.Fatal("máquina em duas etapas aceita")
	}
	if _, err := st.CreateLocatedCampaign(ctx, c, []campaign.Target{{MachineID: "m9", Stage: 2, Via: campaign.ViaBuilding}}); err == nil {
		t.Fatal("etapa inexistente aceita")
	}
	id, err := st.CreateLocatedCampaign(ctx, c, tg)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Campaign(ctx, id)
	if err != nil || !ok || got.Scope == nil || got.Scope.Pilot.FloorKey != "andar:1º andar" || len(got.Scope.Stages) != 2 || len(got.Rings) != 0 {
		t.Fatalf("escopo gravado: %+v %v", got, err)
	}
	if l, _ := st.CampaignTargets(ctx, id, 1); len(l) != 2 || l[0].MachineID != "m2" || l[1].Via != campaign.ViaUnlocated || l[1].BuildingID != 0 {
		t.Fatalf("alvos da etapa 1: %+v", l)
	}
	if l, _ := st.CampaignTargets(ctx, id, -1); len(l) != 3 || l[0].Floor != "1º andar" || l[0].Stage != 0 {
		t.Fatalf("todos os alvos: %+v", l)
	}
}

func TestPrediosIDMemoria(t *testing.T) { testarPrediosID(t, NewMemory()) }

func TestPrediosIDPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarPrediosID(t, NewPostgres(pool))
}

func TestRenomeacaoDePredio(t *testing.T) {
	old := append(bens("A", "1", "2", "3"), bens("B", "4")...)
	for nome, c := range map[string]struct {
		cur   []AssetLocation
		known map[string]bool
		want  map[string]string
	}{
		"mesmos bens":           {append(bens("A2", "1", "2", "3"), bens("B", "4")...), nil, map[string]string{"A": "A2"}},
		"nome já existia":       {bens("B", "1", "2", "3", "4"), map[string]bool{"B": true}, map[string]string{}},
		"menos da metade ficou": {append(bens("A2", "1"), bens("B", "4")...), nil, map[string]string{}},
		"o novo tem outros bens": {append(bens("A2", "1", "2", "3", "8", "9"), bens("B", "4")...), nil,
			map[string]string{}},
		"dois somem no mesmo": {bens("C", "1", "2", "3", "4"), nil, map[string]string{}},
		"continua":            {append(bens("A", "1"), bens("A2", "2", "3")...), nil, map[string]string{}},
	} {
		got := renameOf(old, c.cur, c.known)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v", nome, got)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %v", nome, got)
			}
		}
	}
}
