package campaign_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

// Um parque fictício: o prédio 1 (Sede) com o 1º andar (s1, s2), o 2º (s3) e uma em conflito
// (s4); o prédio 2 (Fórum) com f1, f2 sem andar; uma máquina sem localização (x1).
func parqueFicticio() []campaign.Candidate {
	sede := func(id, floor string, conflict bool) campaign.Candidate {
		l := campaign.Location{BuildingID: 1, Building: "PREDIO SEDE", Conflict: conflict}
		if floor != "" {
			l.FloorKey, l.Floor = "andar:"+floor, floor
		}
		return campaign.Candidate{MachineID: id, Loc: l}
	}
	return []campaign.Candidate{sede("s1", "1º andar", false), sede("s2", "1º andar", false), sede("s3", "2º andar", false), sede("s4", "", true),
		{MachineID: "f1", Loc: campaign.Location{BuildingID: 2, Building: "FORUM GERAL"}},
		{MachineID: "f2", Loc: campaign.Location{BuildingID: 2, Building: "FORUM GERAL"}},
		{MachineID: "x1"}, {MachineID: "s1", Loc: campaign.Location{BuildingID: 2}}} // a repetida conta uma vez
}

var (
	sedeB  = campaign.ScopeBuilding{ID: 1, Name: "PREDIO SEDE"}
	forumB = campaign.ScopeBuilding{ID: 2, Name: "FORUM GERAL"}
	andar1 = campaign.ScopeFloor{BuildingID: 1, Building: "PREDIO SEDE", Key: "andar:1º andar", Label: "1º andar"}
	andar2 = campaign.ScopeFloor{BuildingID: 1, Building: "PREDIO SEDE", Key: "andar:2º andar", Label: "2º andar"}
)

func ids(ts []campaign.Target, stage int) string {
	var out []string
	for _, t := range ts {
		if stage < 0 || t.Stage == stage {
			out = append(out, t.MachineID)
		}
	}
	return strings.Join(out, ",")
}

func TestPlanejamentoDoEscopo(t *testing.T) {
	fleet := parqueFicticio()
	for nome, c := range map[string]struct {
		sel    campaign.Selection
		want   string // todos os alvos
		pilot  string // etapa 0 com piloto
		err    error
		stages int
	}{
		"um prédio (com sem andar e em conflito)": {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB}}, want: "s1,s2,s3,s4", stages: 1},
		"dois prédios":                     {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}}, want: "f1,f2,s1,s2,s3,s4", stages: 1},
		"um andar":                         {sel: campaign.Selection{Floors: []campaign.ScopeFloor{andar1}}, want: "s1,s2", stages: 1},
		"dois andares":                     {sel: campaign.Selection{Floors: []campaign.ScopeFloor{andar1, andar2}}, want: "s1,s2,s3", stages: 1},
		"prédio e andar dele, sem repetir": {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB}, Floors: []campaign.ScopeFloor{andar1}}, want: "s1,s2,s3,s4", stages: 1},
		"sem localização só se pedir":      {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}, Unlocated: true}, want: "f1,f2,x1", stages: 1},
		"piloto por andar":                 {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: 1, Building: "PREDIO SEDE", FloorKey: "andar:1º andar", FloorLabel: "1º andar"}}, want: "f1,f2,s1,s2,s3,s4", pilot: "s1,s2", stages: 2},
		"piloto por prédio":                {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 2, Building: "FORUM GERAL"}}, want: "f1,f2,s1,s2,s3,s4", pilot: "f1,f2", stages: 2},
		"piloto fora do escopo":            {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 1}}, err: campaign.ErrPilotOutside},
		"andar piloto de outro prédio":     {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: 1, FloorKey: "andar:1º andar"}}, err: campaign.ErrPilotOutside},
		"piloto sem equipamentos":          {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: 1, FloorKey: "andar:9º andar"}}, err: campaign.ErrPilotEmpty},
		"piloto é o escopo todo":           {sel: campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 2}}, err: campaign.ErrPilotWholeScope},
		"nada":                             {sel: campaign.Selection{Floors: []campaign.ScopeFloor{{BuildingID: 9, Key: "andar:x"}}}, err: campaign.ErrEmptyScope},
	} {
		sc, ts, err := campaign.PlanScope(c.sel, fleet)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%s: erro %v, esperado %v", nome, err, c.err)
			}
			continue
		}
		if err != nil || ids(ts, -1) != c.want || len(sc.Stages) != c.stages {
			t.Errorf("%s: %s (%d etapas) %v", nome, ids(ts, -1), len(sc.Stages), err)
			continue
		}
		if c.pilot != "" && (ids(ts, 0) != c.pilot || !sc.Stages[0].Pilot || sc.Stages[0].Machines != len(strings.Split(c.pilot, ","))) {
			t.Errorf("%s: piloto %s %+v", nome, ids(ts, 0), sc.Stages)
		}
	}
	// No andar escolhido, a máquina em conflito fica fora e é contada.
	sc, ts, _ := campaign.PlanScope(campaign.Selection{Floors: []campaign.ScopeFloor{andar1}}, fleet)
	if sc.Excluded[campaign.ExcludedConflict] != 1 || slices.ContainsFunc(ts, func(t campaign.Target) bool { return t.MachineID == "s4" }) {
		t.Errorf("conflito: %+v", sc.Excluded)
	}
	// Sem localização nunca vai para o piloto.
	_, ts, _ = campaign.PlanScope(campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}, Unlocated: true,
		Pilot: &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 2}}, fleet)
	if ids(ts, 0) != "f1,f2" || ids(ts, 1) != "x1" {
		t.Errorf("sem localização no piloto: %s / %s", ids(ts, 0), ids(ts, 1))
	}
	// Mais de dez prédios numa campanha: nenhum limite de grupos.
	var big []campaign.Candidate
	var sel campaign.Selection
	for i := 1; i <= 25; i++ {
		big = append(big, campaign.Candidate{MachineID: fmt.Sprintf("m%02d", i), Loc: campaign.Location{BuildingID: int64(i), Building: fmt.Sprintf("PREDIO %d", i)}})
		sel.Buildings = append(sel.Buildings, campaign.ScopeBuilding{ID: int64(i), Name: fmt.Sprintf("PREDIO %d", i)})
	}
	sel.Pilot = &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 7, Building: "PREDIO 7"}
	sc, ts, err := campaign.PlanScope(sel, big)
	if err != nil || len(ts) != 25 || ids(ts, 0) != "m07" || sc.Stages[1].Machines != 24 {
		t.Fatalf("25 prédios: %v %+v", err, sc.Stages)
	}
	if c := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, TTL: time.Hour, Scope: &sc}); c.Validate() != nil {
		t.Errorf("validação: %v", c.Validate())
	}
	if c := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, TTL: time.Hour, Scope: &sc, Rings: []int{0}}); c.Validate() == nil {
		t.Error("por localização com grupos aceita")
	}
}

// --- O motor numa campanha por localização ---------------------------------------------

type ambienteLocal struct {
	*ambiente
	locs    map[string]campaign.Location
	windows map[int64]window.Window
}

func novoAmbienteLocal(t *testing.T) *ambienteLocal {
	a := &ambienteLocal{ambiente: novoAmbiente(t), windows: map[int64]window.Window{}}
	ctx := context.Background()
	a.locs = map[string]campaign.Location{}
	for _, c := range parqueFicticio()[:7] {
		if _, err := a.st.SaveInventory(ctx, c.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: c.MachineID}); err != nil {
			t.Fatal(err)
		}
		a.locs[c.MachineID] = c.Loc
	}
	a.e.Locate = func(_ context.Context, list []string) (map[string]campaign.Location, error) {
		out := map[string]campaign.Location{}
		for _, id := range list {
			if l, ok := a.locs[id]; ok {
				out[id] = l
			}
		}
		return out, nil
	}
	a.e.BuildingWindows = func(context.Context) (map[int64]window.Window, error) { return a.windows, nil }
	return a
}

func (a *ambienteLocal) criaLocal(c campaign.Campaign, sel campaign.Selection) int64 {
	a.t.Helper()
	var fleet []campaign.Candidate
	for _, x := range parqueFicticio()[:7] {
		fleet = append(fleet, campaign.Candidate{MachineID: x.MachineID, Loc: a.locs[x.MachineID]})
	}
	sc, ts, err := campaign.PlanScope(sel, fleet)
	if err != nil {
		a.t.Fatal(err)
	}
	c.Scope = &sc
	if err := c.Validate(); err != nil {
		a.t.Fatal(err)
	}
	id, err := a.st.CreateLocatedCampaign(context.Background(), c, ts)
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

func (a *ambienteLocal) jobsDe(id int64, idx int) map[string]campaign.JobState {
	l, _ := a.st.CampaignJobs(context.Background(), id, idx)
	out := map[string]campaign.JobState{}
	for _, j := range l {
		out[j.MachineID] = j
	}
	return out
}

func (a *ambienteLocal) encerraTodos(id int64, idx int, estado string) {
	for _, j := range a.jobsDe(id, idx) {
		if !jobs.Final(j.State) {
			_, _ = a.st.FinishJob(context.Background(), j.ID, estado, "teste", a.agora)
		}
	}
}

var pilotoAndar1 = &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: 1, Building: "PREDIO SEDE", FloorKey: "andar:1º andar", FloorLabel: "1º andar"}

// O piloto por andar libera só o andar; depois da observação, o resto do escopo vai junto (em
// paralelo), cada máquina na janela do próprio prédio, sem repetir o piloto.
func TestCampanhaPorPredioComPilotoPorAndar(t *testing.T) {
	a := novoAmbienteLocal(t)
	loc, _ := time.LoadLocation("America/Porto_Velho")
	a.windows[1] = window.Window{Days: window.AllDays, Start: 12 * time.Hour, Duration: 2 * time.Hour, Location: loc}
	a.windows[2] = window.Window{Days: window.AllDays, Start: 22 * time.Hour, Duration: 6 * time.Hour, Location: loc} // passa da meia-noite
	id := a.criaLocal(campaign.Campaign{Name: "por prédio", Type: jobs.TypeRescan, UseWindow: true, Observe: 24 * time.Hour, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: pilotoAndar1})

	c := a.passo(id, 0)
	p := a.jobsDe(id, 0)
	if len(p) != 2 || p["s1"].ID == 0 || p["s3"].ID != 0 || len(a.jobsDe(id, 1)) != 0 {
		t.Fatalf("o piloto é só o 1º andar: %+v", p)
	}
	// A janela da Sede: 12:00 em Porto Velho (UTC-4) = 16:00 UTC.
	if got := p["s1"].NotBefore.UTC().Format("15:04"); got != "16:00" {
		t.Errorf("janela do prédio no piloto: %s", got)
	}
	if !strings.Contains(c.Notes[0], "Piloto — Predio Sede, 1º andar: 2 job(s)") {
		t.Errorf("nota: %v", c.Notes)
	}
	a.encerraTodos(id, 0, jobs.StateDone)
	c = a.passo(id, time.Minute)
	if !strings.Contains(c.Detail, "observando até") || len(a.jobsDe(id, 1)) != 0 {
		t.Fatalf("observação: %+v", c)
	}
	c = a.passo(id, 25*time.Hour)
	g := a.jobsDe(id, 1)
	if len(g) != 4 || g["s1"].ID != 0 || g["s2"].ID != 0 || g["f1"].ID == 0 || g["s4"].ID == 0 {
		t.Fatalf("etapa geral, sem o piloto: %+v", g)
	}
	// Cada um na janela do seu prédio: o Fórum às 22:00 locais (02:00 UTC do dia seguinte).
	if g["f1"].NotBefore.UTC().Format("15:04") != "02:00" || g["s3"].NotBefore.UTC().Format("15:04") != "16:00" {
		t.Errorf("janelas individuais: f1 %v, s3 %v", g["f1"].NotBefore, g["s3"].NotBefore)
	}
	a.encerraTodos(id, 1, jobs.StateDone)
	if c = a.passo(id, 26*time.Hour); c.State != campaign.StateDone || !strings.Contains(c.Detail, "todas as etapas concluídas") {
		t.Fatalf("fim: %+v", c)
	}
}

// Falha no piloto: pausa e não libera o resto; um piloto sem nenhum job bem-sucedido também
// pausa, pedindo decisão; a retomada aceita e segue.
func TestPilotoComFalhaOuSemEvidencia(t *testing.T) {
	a := novoAmbienteLocal(t)
	id := a.criaLocal(campaign.Campaign{Name: "falha", Type: jobs.TypeRescan, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: 2, Building: "FORUM GERAL"}})
	a.passo(id, 0)
	a.encerraTodos(id, 0, jobs.StateFailed)
	c := a.passo(id, time.Minute)
	if c.State != campaign.StatePaused || len(a.jobsDe(id, 1)) != 0 {
		t.Fatalf("falha no piloto: %+v", c)
	}

	// update sem pendência em nenhuma máquina do piloto: zero jobs, nada de liberação silenciosa.
	b := novoAmbienteLocal(t)
	b.e.Pending = func(context.Context, string) ([]protocol.PendingItem, bool, error) { return nil, true, nil }
	id = b.criaLocal(campaign.Campaign{Name: "vazio", Type: jobs.TypeUpdate, Sources: []string{"curl"}, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: pilotoAndar1})
	c = b.passo(id, 0)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "sem evidência") || len(b.jobsDe(id, 1)) != 0 {
		t.Fatalf("piloto sem jobs aplicáveis: %+v", c)
	}
	if err := campaign.Resume(&c); err != nil {
		t.Fatal(err)
	}
	_, _ = b.st.SaveCampaign(context.Background(), c, campaign.StatePaused)
	if c = b.passo(id, time.Minute); c.CurrentIdx != 1 || c.LaunchedIdx != 1 {
		t.Fatalf("a retomada é a decisão do admin: %+v", c)
	}
}

// A janela removida no meio da campanha impede a liberação da etapa (nada sai, nem para os
// prédios com janela); a máquina que mudou de prédio fica fora; a aposentada também; e uma
// nova instância do motor continua do mesmo ponto, sem repetir jobs.
func TestEtapaGeralComJanelaRemovidaEMovidas(t *testing.T) {
	a := novoAmbienteLocal(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("America/Porto_Velho")
	w := window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: 3 * time.Hour, Location: loc}
	a.windows[1], a.windows[2] = w, w
	id := a.criaLocal(campaign.Campaign{Name: "noite", Type: jobs.TypeRescan, UseWindow: true, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: pilotoAndar1})
	a.passo(id, 0)
	a.encerraTodos(id, 0, jobs.StateDone)
	delete(a.windows, 2)
	c := a.passo(id, time.Minute)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "Forum Geral") || len(a.jobsDe(id, 1)) != 0 {
		t.Fatalf("sem janela no Fórum: %+v", c)
	}
	a.windows[2] = w
	// s3 foi para o Fórum; f2 foi aposentada.
	a.locs["s3"] = campaign.Location{BuildingID: 2, Building: "FORUM GERAL"}
	if ok, _ := a.st.RetireMachine(ctx, "f2", "teste", "t"); !ok {
		t.Fatal("aposentar")
	}
	if err := campaign.Resume(&c); err != nil {
		t.Fatal(err)
	}
	_, _ = a.st.SaveCampaign(ctx, c, campaign.StatePaused)
	// Outro servidor (reinício): o mesmo banco, um motor novo.
	e2 := *a.e
	a.e = &e2
	c = a.passo(id, 2*time.Minute)
	g := a.jobsDe(id, 1)
	if len(g) != 2 || g["f1"].ID == 0 || g["s4"].ID == 0 {
		t.Fatalf("etapa geral: %+v", g)
	}
	note := c.Notes[len(c.Notes)-1]
	if !strings.Contains(note, "1 "+campaign.SkipMovedBuilding) || !strings.Contains(note, "1 aposentada") {
		t.Errorf("motivos: %s", note)
	}
	if c = a.passo(id, 3*time.Minute); len(a.jobsDe(id, 1)) != 2 {
		t.Error("repetiu jobs")
	}
}

// Reenvio manual e automático ficam na etapa e no escopo originais: a que mudou de andar no
// piloto por andar não recebe a nova tentativa; o Wake on LAN só acorda a etapa liberada.
func TestReenvioEWakeNaEtapa(t *testing.T) {
	a := novoAmbienteLocal(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("America/Porto_Velho")
	inicio := a.agora.Add(30 * time.Minute).In(loc)
	a.windows[1] = window.Window{Days: window.AllDays, Start: time.Duration(inicio.Hour())*time.Hour + time.Duration(inicio.Minute())*time.Minute, Duration: 3 * time.Hour, Location: loc}
	a.windows[2] = a.windows[1]
	// Todas desligadas: o último contato foi há 3 h.
	a.st.SetClock(func() time.Time { return a.agora.Add(-3 * time.Hour) })
	for m := range a.locs {
		if _, err := a.st.SaveInventory(ctx, m, protocol.InventoryReport{SchemaVersion: 1, Hash: m + "-2"}); err != nil {
			t.Fatal(err)
		}
	}
	var acordou []string
	a.e.Wake = func(_ context.Context, id, _ string) (int64, string, error) {
		acordou = append(acordou, id)
		return 0, "sem repetidor (teste)", nil
	}
	id := a.criaLocal(campaign.Campaign{Name: "reenvio", Type: jobs.TypeRescan, UseWindow: true, Wake: true, AutoResend: 1, Observe: 24 * time.Hour, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{sedeB, forumB}, Pilot: pilotoAndar1})
	a.passo(id, 0)
	a.passo(id, 25*time.Minute)
	slices.Sort(acordou)
	if strings.Join(acordou, ",") != "s1,s2" {
		t.Fatalf("Wake on LAN só no piloto: %v", acordou)
	}
	// s1 expira: o reenvio automático manda de novo (1 vez); s2 falha de vez.
	p := a.jobsDe(id, 0)
	_, _ = a.st.FinishJob(ctx, p["s1"].ID, jobs.StateExpired, "teste", a.agora)
	_, _ = a.st.FinishJob(ctx, p["s2"].ID, jobs.StateRejected, "teste", a.agora)
	c := a.passo(id, 26*time.Minute)
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 3 || c.State != campaign.StateRunning {
		t.Fatalf("reenvio automático: %d jobs, %+v", len(l), c)
	}
	a.encerraTodos(id, 0, jobs.StateDone)
	if c = a.passo(id, 26*time.Minute+30*time.Second); c.State != campaign.StatePaused || !strings.Contains(c.Detail, "rejeitado") {
		t.Fatalf("o recusado pausa: %+v", c)
	}
	// O admin pede o reenvio dos recusados; s2 mudou de andar: dispensada, sem job novo.
	a.locs["s2"] = campaign.Location{BuildingID: 1, Building: "PREDIO SEDE", FloorKey: "andar:2º andar", Floor: "2º andar"}
	if err := campaign.RequestResend(ctx, a.st, c, []string{campaign.ReasonRejected}, "admin"); err != nil {
		t.Fatal(err)
	}
	c = a.passo(id, 27*time.Minute)
	l, _ = a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 3 || !strings.Contains(strings.Join(c.Notes, " "), "reenvio pedido") {
		t.Fatalf("reenvio da que saiu do andar: %d jobs %v", len(l), c.Notes)
	}
	var dispensado bool
	for _, j := range l {
		dispensado = dispensado || (j.MachineID == "s2" && j.State == jobs.StateDismissed && strings.Contains(j.Detail, campaign.SkipMovedFloor))
	}
	if !dispensado {
		t.Errorf("s2 dispensada pelo andar: %+v", l)
	}
	if len(a.jobsDe(id, 1)) != 0 {
		t.Error("o reenvio liberou a etapa seguinte")
	}
	_ = store.MaxWindowBatch
}

// Um lançamento interrompido no meio (o servidor caiu): o seguinte não repete a máquina que já
// recebeu o job.
func TestLancamentoInterrompidoNaoRepete(t *testing.T) {
	a := novoAmbienteLocal(t)
	ctx := context.Background()
	id := a.criaLocal(campaign.Campaign{Name: "x", Type: jobs.TypeRescan, TTL: time.Hour, CreatedBy: "t"},
		campaign.Selection{Buildings: []campaign.ScopeBuilding{forumB}})
	if _, err := jobs.Issue(ctx, a.st, a.e.Key, jobs.Request{MachineID: "f1", Type: jobs.TypeRescan, TTL: time.Hour, By: "campanha",
		CampaignID: id, CampaignIdx: 0}, a.agora); err != nil {
		t.Fatal(err)
	}
	a.passo(id, 0)
	if l, _ := a.st.CampaignJobs(ctx, id, 0); len(l) != 2 {
		t.Fatalf("jobs: %+v", l)
	}
}
