package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/texto"
)

// O escopo das campanhas por localização no painel: o formulário (prédios recolhidos, com os
// andares dentro; todos os prédios; os sem localização, com confirmação; o piloto), a leitura e
// a validação do que o navegador mandou (só IDs e chaves que o servidor conhece) e o plano
// (campaign.PlanScope). A conferência e a criação recalculam o plano no servidor: o navegador
// nunca manda máquinas nem contagens.

type scopeFloorOpt struct {
	Value, Label string
	Machines     int
	Checked      bool
}

type scopeBuildingOpt struct {
	ID                             int64
	Name                           string
	Machines, Unfloored, Conflicts int
	Checked, Open, NoWindow        bool
	WindowText                     string
	Floors                         []scopeFloorOpt
}

type scopeFloorGroup struct {
	Label   string
	Options []optView
}

// scopeForm: a parte "Escopo" e "Implantação piloto" do formulário de campanha.
type scopeForm struct {
	Buildings              []scopeBuildingOpt
	NBuildings, NMachines  int
	NUnlocated             int
	NNoWindow              int
	All                    bool
	Unlocated, UnlocatedOK bool
	Pilot                  string // "", "predio", "andar"
	PilotBuildings         []optView
	PilotFloors            []scopeFloorGroup
	Q                      string // a pesquisa que filtra a lista
	Hidden                 int    // prédios fora da pesquisa
}

// scopeInput: o que o navegador mandou, ainda sem conferir.
type scopeInput struct {
	Buildings              []string
	Floors                 []string
	All                    bool
	Unlocated, UnlocatedOK bool
	Pilot                  string
	PilotBuilding          string
	PilotFloor             string
}

func readScopeInput(form url.Values) scopeInput {
	return scopeInput{Buildings: form["predio"], Floors: form["andar"], All: form.Get("todos") == "1",
		Unlocated: form.Get("sem_local") == "1", UnlocatedOK: form.Get("sem_local_ok") == "1",
		Pilot: form.Get("piloto"), PilotBuilding: form.Get("piloto_predio"), PilotFloor: form.Get("piloto_andar")}
}

// values: o escopo para os campos escondidos da conferência.
func (in scopeInput) values() []optView {
	var out []optView
	add := func(k, v string) { out = append(out, optView{Value: k, Label: v}) }
	for _, b := range in.Buildings {
		add("predio", b)
	}
	for _, f := range in.Floors {
		add("andar", f)
	}
	if in.All {
		add("todos", "1")
	}
	if in.Unlocated {
		add("sem_local", "1")
	}
	if in.UnlocatedOK {
		add("sem_local_ok", "1")
	}
	if in.Pilot != "" {
		add("piloto", in.Pilot)
		add("piloto_predio", in.PilotBuilding)
		add("piloto_andar", in.PilotFloor)
	}
	return out
}

// scopeFormFrom monta o formulário com a escolha in (vazia: nada marcado).
func scopeFormFrom(o *deployOverview, in scopeInput, q string) scopeForm {
	f := scopeForm{All: in.All, Unlocated: in.Unlocated, UnlocatedOK: in.UnlocatedOK, Pilot: in.Pilot, NUnlocated: o.Unlocated, Q: q}
	chosenB := map[string]bool{}
	for _, b := range in.Buildings {
		chosenB[b] = true
	}
	chosenF := map[string]bool{}
	for _, x := range in.Floors {
		chosenF[x] = true
	}
	needle := strings.ToLower(strings.TrimSpace(q))
	for _, b := range o.Buildings {
		if !b.Present && b.Machines == 0 {
			continue
		}
		id := strconv.FormatInt(b.ID, 10)
		opt := scopeBuildingOpt{ID: b.ID, Name: b.Name, Machines: b.Machines, Unfloored: b.Unfloored, Conflicts: b.Conflicts,
			Checked: chosenB[id], NoWindow: b.WindowState != "ok", WindowText: b.WindowText}
		if b.WindowState == "desativada" {
			opt.WindowText += " (desativada)"
		}
		f.NBuildings++
		f.NMachines += b.Machines
		if opt.NoWindow && b.Machines > 0 {
			f.NNoWindow++
		}
		for _, fl := range b.Floors {
			if fl.Machines == 0 {
				continue
			}
			fo := scopeFloorOpt{Value: fl.Value, Label: fl.Label, Machines: fl.Machines, Checked: chosenF[fl.Value]}
			opt.Open = opt.Open || fo.Checked
			opt.Floors = append(opt.Floors, fo)
		}
		visible := needle == "" || strings.Contains(strings.ToLower(b.Name), needle) || strings.Contains(strings.ToLower(texto.PlaceName(b.Name)), needle)
		if b.Machines > 0 {
			f.PilotBuildings = append(f.PilotBuildings, optView{Value: id, Label: fmt.Sprintf("%s · %d equip.", texto.PlaceName(b.Name), b.Machines), Selected: in.PilotBuilding == id})
		}
		if len(opt.Floors) > 0 {
			g := scopeFloorGroup{Label: texto.PlaceName(b.Name)}
			for _, fo := range opt.Floors {
				g.Options = append(g.Options, optView{Value: fo.Value, Label: fmt.Sprintf("%s · %d equip.", fo.Label, fo.Machines), Selected: in.PilotFloor == fo.Value})
			}
			f.PilotFloors = append(f.PilotFloors, g)
		}
		if !visible && !opt.Checked && !opt.Open {
			f.Hidden++
			continue
		}
		f.Buildings = append(f.Buildings, opt)
	}
	return f
}

// selectionFrom confere a escolha contra os prédios e andares que existem agora.
func selectionFrom(o *deployOverview, in scopeInput) (campaign.Selection, string) {
	var sel campaign.Selection
	seenB := map[int64]bool{}
	addB := func(b *deployBuilding) {
		if !seenB[b.ID] {
			seenB[b.ID] = true
			sel.Buildings = append(sel.Buildings, campaign.ScopeBuilding{ID: b.ID, Name: b.Name})
		}
	}
	if in.All {
		for i := range o.Buildings {
			if o.Buildings[i].Machines > 0 {
				addB(&o.Buildings[i])
			}
		}
	}
	if len(in.Buildings) > 5000 || len(in.Floors) > 5000 {
		return sel, "Escolha demais no formulário."
	}
	for _, x := range in.Buildings {
		id, err := strconv.ParseInt(x, 10, 64)
		b, ok := o.building(id)
		if err != nil || !ok {
			return sel, "Prédio desconhecido no escopo: recarregue a página e escolha de novo."
		}
		addB(b)
	}
	seenF := map[string]bool{}
	for _, x := range in.Floors {
		id, key, ok := parseFloorValue(x)
		b, found := o.building(id)
		if !ok || !found {
			return sel, "Andar desconhecido no escopo: recarregue a página e escolha de novo."
		}
		fl, ok := b.floor(key)
		if !ok {
			return sel, fmt.Sprintf("O andar escolhido não existe mais em %s.", texto.PlaceName(b.Name))
		}
		if !seenF[x] {
			seenF[x] = true
			sel.Floors = append(sel.Floors, campaign.ScopeFloor{BuildingID: b.ID, Building: b.Name, Key: fl.Key, Label: fl.Label})
		}
	}
	if in.Unlocated {
		if !in.UnlocatedOK {
			return sel, "Para incluir os equipamentos sem localização identificada, marque também a confirmação ao lado."
		}
		sel.Unlocated = true
	}
	if len(sel.Buildings) == 0 && len(sel.Floors) == 0 && !sel.Unlocated {
		return sel, "Escolha o escopo: todos os prédios, prédios inteiros ou andares."
	}
	switch in.Pilot {
	case "":
	case campaign.PilotBuilding:
		id, err := strconv.ParseInt(in.PilotBuilding, 10, 64)
		b, ok := o.building(id)
		if err != nil || !ok {
			return sel, "Escolha o prédio piloto."
		}
		sel.Pilot = &campaign.Pilot{Kind: campaign.PilotBuilding, BuildingID: b.ID, Building: b.Name}
	case campaign.PilotFloor:
		id, key, ok := parseFloorValue(in.PilotFloor)
		b, found := o.building(id)
		if !ok || !found {
			return sel, "Escolha o andar piloto."
		}
		fl, ok := b.floor(key)
		if !ok || fl.Machines == 0 {
			return sel, "O andar piloto não existe ou não tem equipamentos identificados."
		}
		sel.Pilot = &campaign.Pilot{Kind: campaign.PilotFloor, BuildingID: b.ID, Building: b.Name, FloorKey: fl.Key, FloorLabel: fl.Label}
	default:
		return sel, "Escolha a implantação piloto da lista."
	}
	return sel, ""
}

// planScope: o escopo resolvido, com a mensagem para a tela.
func planScope(o *deployOverview, sel campaign.Selection) (campaign.Scope, []campaign.Target, string) {
	cands := make([]campaign.Candidate, len(o.fleet))
	for i, e := range o.fleet {
		cands[i] = campaign.Candidate{MachineID: e.ID, Loc: e.Loc.campaignLocation()}
	}
	sc, ts, err := campaign.PlanScope(sel, cands)
	switch {
	case errors.Is(err, campaign.ErrEmptyScope):
		return sc, nil, "Nenhum equipamento no escopo escolhido."
	case errors.Is(err, campaign.ErrPilotOutside):
		return sc, nil, "O piloto precisa ser um prédio ou um andar do escopo escolhido."
	case errors.Is(err, campaign.ErrPilotEmpty):
		return sc, nil, "O piloto não tem equipamentos elegíveis (andar em conflito não entra no piloto)."
	case errors.Is(err, campaign.ErrPilotWholeScope):
		return sc, nil, "O piloto cobre todo o escopo: escolha \"sem piloto\" ou amplie o escopo."
	case err != nil:
		return sc, nil, err.Error()
	}
	return sc, ts, ""
}

// scopeDigest: a impressão do plano (etapas e alvos). A conferência manda; a criação recalcula
// e, se mudou, mostra a conferência de novo.
func scopeDigest(sc campaign.Scope, ts []campaign.Target) string {
	h := sha256.New()
	for _, st := range sc.Stages {
		fmt.Fprintf(h, "etapa\x00%s\x00%t\x00%d\n", st.Label, st.Pilot, st.Machines)
	}
	for _, t := range ts {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%s\x00%s\n", t.MachineID, t.Stage, t.BuildingID, t.FloorKey, t.Via)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// --- A conferência -----------------------------------------------------------------------

type confirmBuilding struct {
	Name               string
	Total, Pilot, Rest int
	Window, Next       string
	NoWindow           bool
}

type confirmStage struct {
	Label     string
	Machines  int
	Buildings int
	Pilot     bool
	Preview   string
}

type scopeConfirm struct {
	Stages            []confirmStage
	Buildings         []confirmBuilding
	Floors            []string
	PilotL            string
	PilotN, RestN     int
	UnlocatedN, Total int
	Excluded          []string
	Digest            string
	Hidden            []optView
	MissingWindow     []string
}

func buildScopeConfirm(o *deployOverview, sc campaign.Scope, ts []campaign.Target, in scopeInput) scopeConfirm {
	v := scopeConfirm{Digest: scopeDigest(sc, ts), Hidden: in.values(), Total: len(ts)}
	type agg struct{ total, pilot, rest int }
	by := map[int64]*agg{}
	stageB := map[int]map[int64]bool{}
	for _, t := range ts {
		if stageB[t.Stage] == nil {
			stageB[t.Stage] = map[int64]bool{}
		}
		if t.BuildingID == 0 {
			v.UnlocatedN++
		} else {
			stageB[t.Stage][t.BuildingID] = true
		}
		if t.BuildingID == 0 {
			continue
		}
		g := by[t.BuildingID]
		if g == nil {
			g = &agg{}
			by[t.BuildingID] = g
		}
		g.total++
		if sc.Pilot != nil && t.Stage == 0 {
			g.pilot++
		} else {
			g.rest++
		}
	}
	for i, st := range sc.Stages {
		v.Stages = append(v.Stages, confirmStage{Label: st.Label, Machines: st.Machines, Buildings: len(stageB[i]), Pilot: st.Pilot})
		if st.Pilot {
			v.PilotN = st.Machines
		} else {
			v.RestN += st.Machines
		}
	}
	if sc.Pilot != nil {
		v.PilotL = campaign.PilotLabel(*sc.Pilot)
	}
	for _, b := range o.Buildings {
		g := by[b.ID]
		if g == nil {
			continue
		}
		cb := confirmBuilding{Name: texto.PlaceName(b.Name), Total: g.total, Pilot: g.pilot, Rest: g.rest, Window: dash(b.WindowText), Next: dash(b.Next),
			NoWindow: b.WindowState != "ok"}
		if b.WindowState == "desativada" {
			cb.Window += " (desativada)"
		}
		if cb.NoWindow {
			v.MissingWindow = append(v.MissingWindow, cb.Name)
		}
		v.Buildings = append(v.Buildings, cb)
	}
	for _, f := range sc.Floors {
		v.Floors = append(v.Floors, texto.PlaceName(f.Building)+" — "+f.Label)
	}
	for k, n := range sc.Excluded {
		v.Excluded = append(v.Excluded, fmt.Sprintf("%d: %s", n, k))
	}
	slices.Sort(v.Excluded)
	return v
}

// stageGroups: as máquinas de cada etapa (para as previsões da conferência).
func stageGroups(ts []campaign.Target) map[int][]string {
	out := map[int][]string{}
	for _, t := range ts {
		out[t.Stage] = append(out[t.Stage], t.MachineID)
	}
	return out
}

// scopeAuditText: o escopo, para a auditoria da criação.
func scopeAuditText(sc campaign.Scope) string {
	var parts []string
	var bs []string
	for _, b := range sc.Buildings {
		bs = append(bs, fmt.Sprintf("%d %s", b.ID, texto.PlaceName(b.Name)))
	}
	if len(bs) > 0 {
		parts = append(parts, "prédios: "+strings.Join(bs, "; "))
	}
	var fs []string
	for _, f := range sc.Floors {
		fs = append(fs, fmt.Sprintf("%d %s — %s", f.BuildingID, texto.PlaceName(f.Building), f.Label))
	}
	if len(fs) > 0 {
		parts = append(parts, "andares: "+strings.Join(fs, "; "))
	}
	if sc.Unlocated {
		parts = append(parts, "com os sem localização")
	}
	if sc.Pilot != nil {
		parts = append(parts, "piloto: "+campaign.PilotLabel(*sc.Pilot))
	} else {
		parts = append(parts, "sem piloto")
	}
	var st []string
	for _, s := range sc.Stages {
		st = append(st, fmt.Sprintf("%s (%d)", s.Label, s.Machines))
	}
	parts = append(parts, "etapas: "+strings.Join(st, " → "))
	return strings.Join(parts, " · ")
}

// scopeFromRequest: o formulário inteiro de escopo, lido e resolvido (para a conferência e a
// criação). problem não vazio: a mensagem para a tela.
func (a *api) scopeFromRequest(r *http.Request, o *deployOverview) (scopeInput, campaign.Scope, []campaign.Target, string) {
	in := readScopeInput(r.PostForm)
	sel, problem := selectionFrom(o, in)
	if problem != "" {
		return in, campaign.Scope{}, nil, problem
	}
	sc, ts, problem := planScope(o, sel)
	return in, sc, ts, problem
}

// campaignProgress: o andamento por prédio (e pelo andar piloto) de uma campanha por
// localização, dos alvos gravados e do job mais recente de cada máquina.
func (a *api) campaignProgress(r *http.Request, c campaign.Campaign, stageJobs [][]campaign.JobState, v *campaignView) error {
	ts, err := a.store.CampaignTargets(r.Context(), c.ID, -1)
	if err != nil {
		return err
	}
	wins, err := ActiveBuildingWindows(r.Context(), a.store)
	if err != nil {
		return err
	}
	now := a.now()
	latest := map[string]campaign.JobState{}
	for _, list := range stageJobs {
		for _, j := range list {
			if cur, ok := latest[j.MachineID]; !ok || j.ID > cur.ID {
				latest[j.MachineID] = j
			}
		}
	}
	v.Located, v.StagesL = true, c.StagesText(false)
	if c.Scope.Pilot != nil {
		v.PilotL = campaign.PilotLabel(*c.Scope.Pilot)
		v.StagesL = c.StageLabel(0) + " → observação de " + hoursLabel(int(c.Observe.Hours())) + " → " + c.StageLabel(1)
	}
	v.ScopeL = scopeAuditText(*c.Scope)
	type key struct {
		stage int
		name  string
	}
	rows := map[key]*campaignProgressRow{}
	var order []key
	for _, t := range ts {
		name := texto.PlaceName(t.Building)
		if t.Via == campaign.ViaUnlocated {
			name = "Sem localização identificada"
		}
		if c.PilotStage(t.Stage) && c.Scope.Pilot.Kind == campaign.PilotFloor {
			name += " — " + t.Floor
		}
		k := key{t.Stage, name}
		row := rows[k]
		if row == nil {
			row = &campaignProgressRow{Stage: c.StageLabel(t.Stage), Name: name, Next: "—"}
			if w, ok := wins[t.BuildingID]; ok && c.UseWindow {
				row.Next = windowNext(w, now)
			}
			rows[k] = row
			order = append(order, k)
		}
		row.Total++
		v.TotalTargets++
		pilot := c.PilotStage(t.Stage)
		if pilot {
			v.PilotTotal++
		}
		j, has := latest[t.MachineID]
		switch {
		case t.Stage > c.LaunchedIdx:
			row.WaitPilot++
			v.WaitPN++
		case !has:
			row.NoJob++
		case j.State == jobs.StateDone:
			row.Done++
			v.DoneN++
			if pilot {
				v.PilotOK++
			}
		case j.State == jobs.StatePending && j.NotBefore != nil && j.NotBefore.After(now):
			row.WaitWindow++
			v.WaitWindowN++
		case j.State == jobs.StatePending || j.State == jobs.StateDelivered:
			row.Running++
		case j.State == jobs.StatePostponed:
			row.Postponed++
		case j.State == jobs.StateDismissed:
			row.Dismissed++
		default:
			row.Failed++
		}
	}
	slices.SortFunc(order, func(x, y key) int {
		if x.stage != y.stage {
			return x.stage - y.stage
		}
		return strings.Compare(x.name, y.name)
	})
	for _, k := range order {
		v.Progress = append(v.Progress, *rows[k])
	}
	return nil
}

// windowProblem: na janela de cada prédio, todos os alvos precisam de uma janela ativa (nada de
// janela de grupo antigo, nada de executar já como alternativa).
func windowProblem(o *deployOverview, ts []campaign.Target) string {
	var missing []string
	seen := map[int64]bool{}
	for _, t := range ts {
		if t.BuildingID == 0 {
			return "Os equipamentos sem localização identificada não têm janela de prédio: escolha \"em horário fixo\" ou \"com prazo\", ou não os inclua."
		}
		if seen[t.BuildingID] {
			continue
		}
		seen[t.BuildingID] = true
		if b, ok := o.building(t.BuildingID); !ok || b.WindowState != "ok" {
			missing = append(missing, texto.PlaceName(t.Building))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	slices.Sort(missing)
	return fmt.Sprintf("Prédio(s) sem janela de manutenção ativa: %s. Configure em Automação › Prédios e implantação, ou escolha \"em horário fixo\" ou \"com prazo\".", texto.Clip(strings.Join(missing, ", "), 1500))
}

// ScopeSpec é o escopo de uma campanha pedida pela linha de comando (patchd-server campaign
// create): prédios pelo ID, andares pelo prédio e pelo nome (ou a chave), e o piloto.
type ScopeSpec struct {
	AllBuildings  bool
	Buildings     []int64
	Floors        []FloorRef
	Unlocated     bool // incluir os sem localização (a flag é a confirmação)
	PilotBuilding int64
	PilotFloor    *FloorRef
}

// FloorRef: um andar de um prédio, pelo nome que a tela mostra ou pela chave.
type FloorRef struct {
	BuildingID int64
	Floor      string
}

// ErrScope: o escopo pedido não vale (a mensagem diz por quê).
var ErrScope = errors.New("escopo inválido")

// PlanCampaign resolve o escopo como o painel: o mesmo resolvedor de localização, as mesmas
// validações e o mesmo plano (etapas e alvos). useWindow: confere a janela ativa de cada prédio.
func PlanCampaign(ctx context.Context, st Store, logger *slog.Logger, spec ScopeSpec, useWindow bool) (campaign.Scope, []campaign.Target, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	o, err := a.deployOverview(ctx)
	if err != nil {
		return campaign.Scope{}, nil, err
	}
	floorVal := func(ref FloorRef) string {
		if b, ok := o.building(ref.BuildingID); ok {
			for _, f := range b.Floors {
				if f.Label == ref.Floor || f.Key == ref.Floor {
					return f.Value
				}
			}
		}
		return floorValue(ref.BuildingID, ref.Floor)
	}
	in := scopeInput{All: spec.AllBuildings, Unlocated: spec.Unlocated, UnlocatedOK: spec.Unlocated}
	for _, id := range spec.Buildings {
		in.Buildings = append(in.Buildings, strconv.FormatInt(id, 10))
	}
	for _, f := range spec.Floors {
		in.Floors = append(in.Floors, floorVal(f))
	}
	switch {
	case spec.PilotFloor != nil:
		in.Pilot, in.PilotFloor = campaign.PilotFloor, floorVal(*spec.PilotFloor)
	case spec.PilotBuilding != 0:
		in.Pilot, in.PilotBuilding = campaign.PilotBuilding, strconv.FormatInt(spec.PilotBuilding, 10)
	}
	sel, problem := selectionFrom(o, in)
	if problem != "" {
		return campaign.Scope{}, nil, fmt.Errorf("%w: %s", ErrScope, problem)
	}
	sc, ts, problem := planScope(o, sel)
	if problem == "" && useWindow {
		problem = windowProblem(o, ts)
	}
	if problem != "" {
		return campaign.Scope{}, nil, fmt.Errorf("%w: %s", ErrScope, problem)
	}
	return sc, ts, nil
}

// BuildingSummary é um prédio para a linha de comando (patchd-server building list).
type BuildingSummary struct {
	ID                             int64
	Name                           string
	Present                        bool
	Machines, Unfloored, Conflicts int
	Floors                         []string // "1º andar (12)"
	Window, Next                   string   // "" sem janela
	WindowOn                       bool
}

// Buildings devolve os prédios como a página Prédios e implantação mostra.
func Buildings(ctx context.Context, st Store, logger *slog.Logger) ([]BuildingSummary, int, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	o, err := a.deployOverview(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]BuildingSummary, 0, len(o.Buildings))
	for _, b := range o.Buildings {
		s := BuildingSummary{ID: b.ID, Name: b.Name, Present: b.Present, Machines: b.Machines, Unfloored: b.Unfloored, Conflicts: b.Conflicts,
			Window: b.WindowText, Next: b.Next, WindowOn: b.WindowOn}
		for _, f := range b.Floors {
			s.Floors = append(s.Floors, fmt.Sprintf("%s (%d)", f.Label, f.Machines))
		}
		out = append(out, s)
	}
	return out, o.Unlocated, nil
}
