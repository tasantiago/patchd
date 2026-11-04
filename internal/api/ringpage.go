package api

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/window"
)

// A tela de grupos de implantação: o resumo, com um cartão por grupo, e a página de cada
// grupo, com as máquinas, o adicionar (por filtro ou colando uma lista), as regras (prédio e
// faixa de IP), o nome e a janela. Antes, pôr máquinas num grupo ficava espalhado entre o
// detalhe, o Parque e o Inventário, e a tela não dizia quem estava em cada grupo.

const groupPerPage = 100

// ringNames: o nome de cada grupo que tem nome.
func (a *api) ringNames(ctx context.Context) (map[int]string, error) {
	list, err := a.store.RingNames(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, n := range list {
		out[n.Ring] = n.Name
	}
	return out, nil
}

// groupLabel: "3 · Fórum Cível", ou o rótulo de sempre ("0 (piloto)") sem nome.
func groupLabel(ring int, names map[int]string) string {
	if n := names[ring]; n != "" {
		return fmt.Sprintf("%d · %s", ring, n)
	}
	return ringLabel(ring)
}

func groupPath(ring int, tab string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	if tab != "" && tab != "maquinas" {
		q.Set("aba", tab)
	}
	p := "/painel/grupos-implantacao/" + strconv.Itoa(ring)
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return p
}

// groupOptions: os grupos para os selects, com o nome.
func groupOptions(names map[int]string, selected int) []optView {
	out := make([]optView, 0, store.MaxRing+1)
	for r := 0; r <= store.MaxRing; r++ {
		out = append(out, optView{Value: strconv.Itoa(r), Label: groupLabel(r, names), Selected: r == selected})
	}
	return out
}

// windowTexts: a janela e a próxima abertura, como a tela mostra.
func windowTexts(windows []store.RingWindow, ring int, now time.Time) (win, next, by string, has bool) {
	rw, has := store.WindowFor(windows, ring)
	if !has {
		return "sem janela", "—", "", false
	}
	s, e := rw.Window.Next(now, window.MinDuration)
	loc := rw.Window.Location
	next = s.In(loc).Format("02/01 15:04")
	if !s.After(now) {
		next = "aberta até " + e.In(loc).Format("15:04")
	}
	return rw.Window.String(), next, rw.UpdatedBy + " · " + ago(now.Sub(rw.UpdatedAt)), true
}

// --- Contexto comum: máquinas, exceções e regras --------------------------------------

type groupFleet struct {
	all   []equip
	pins  map[string]bool
	rules ringRules
	names map[int]string
}

func (a *api) loadGroupFleet(ctx context.Context) (groupFleet, error) {
	var g groupFleet
	var err error
	if g.all, err = a.fleetEquip(ctx); err != nil {
		return g, err
	}
	if g.pins, err = a.store.RingPins(ctx); err != nil {
		return g, err
	}
	if g.rules, err = a.loadRingRules(ctx); err != nil {
		return g, err
	}
	g.names, err = a.ringNames(ctx)
	return g, err
}

// origin: como a máquina está no grupo dela.
type origin struct {
	Key, Label, Class, Conflict string
}

func (g groupFleet) originOf(e equip) origin {
	d := g.rules.decide(e)
	o := origin{Conflict: d.Conflict}
	switch {
	case g.pins[e.ID]:
		o.Key, o.Label, o.Class = "mao", "à mão", "reboot_pendente"
		if d.OK && d.Ring != e.Ring {
			o.Label += fmt.Sprintf(" (a regra diria grupo %d)", d.Ring)
		}
	case d.OK && d.Ring == e.Ring && d.Source == "predio":
		o.Key, o.Label, o.Class = "predio", "pelo prédio", "em_dia"
	case d.OK && d.Ring == e.Ring:
		o.Key, o.Label, o.Class = "faixa", "pela faixa "+d.Rule, "em_dia"
	case d.OK:
		o.Key, o.Class = d.Source, "sem_dado_recente"
		o.Label = fmt.Sprintf("vai para o grupo %d (próxima passada)", d.Ring)
	case e.Ring == store.DefaultRing:
		o.Key, o.Label, o.Class = "padrao", "padrão", "sem_dado_recente"
	default:
		o.Key, o.Label, o.Class = "padrao", "sem regra", "sem_dado_recente"
	}
	return o
}

// --- Resumo --------------------------------------------------------------------------

type groupCard struct {
	Ring                        int
	Label, Name, Href           string
	Machines, Pinned, Conflicts int
	Window, Next                string
	HasWindow                   bool
	Buildings, Ranges           int
}

type ringsView struct {
	pageBase
	Cards []groupCard
	Empty []groupCard // grupos sem nada: só o link
	Total int
}

// ringsPage: o resumo, um cartão por grupo em uso.
func (a *api) ringsPage(w http.ResponseWriter, r *http.Request) {
	v := ringsView{pageBase: a.base(r, "Grupos de implantação e janelas", "aneis")}
	ringsFlash(r.URL.Query(), &v.pageBase)
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os grupos de implantação", err)
		return
	}
	windows, err := a.store.Windows(r.Context())
	if err != nil {
		a.internalError(w, r, "listar janelas", err)
		return
	}
	cards := make([]groupCard, store.MaxRing+1)
	now := a.now()
	for ring := range cards {
		c := &cards[ring]
		c.Ring, c.Label, c.Name, c.Href = ring, groupLabel(ring, g.names), g.names[ring], groupPath(ring, "", nil)
		c.Window, c.Next, _, c.HasWindow = windowTexts(windows, ring, now)
	}
	for _, e := range g.all {
		if e.Ring < 0 || e.Ring > store.MaxRing {
			continue
		}
		c := &cards[e.Ring]
		c.Machines++
		if g.pins[e.ID] {
			c.Pinned++
		}
		if g.rules.decide(e).Conflict != "" {
			c.Conflicts++
		}
	}
	for _, ring := range g.rules.buildings {
		cards[ring].Buildings++
	}
	for _, ip := range g.rules.ips {
		cards[ip.Ring].Ranges++
	}
	v.Total = len(g.all)
	for _, c := range cards {
		if c.Machines > 0 || c.HasWindow || c.Buildings > 0 || c.Ranges > 0 || c.Name != "" || c.Ring <= store.DefaultRing {
			v.Cards = append(v.Cards, c)
		} else {
			v.Empty = append(v.Empty, c)
		}
	}
	a.render(w, r, http.StatusOK, "aneis", v)
}

// --- Página do grupo -----------------------------------------------------------------

// groupQuery: o filtro das listas da página do grupo.
type groupQuery struct {
	Tab      string // maquinas, adicionar, colar, regras
	Q        string // nome, ID, IP, tombo ou local; ou uma faixa de IP
	Building string
	Origin   string // maquinas: mao, predio, faixa, padrao, conflito
	From     int    // adicionar: o grupo de onde vem (-1: qualquer)
	Page     int
}

var groupTabs = []string{"maquinas", "adicionar", "colar", "regras"}

var originFilters = []struct{ V, L string }{
	{"", "Qualquer origem"}, {"mao", "Movidas à mão"}, {"predio", "Pelo prédio"}, {"faixa", "Pela faixa de IP"},
	{"padrao", "Sem regra"}, {"conflito", "Com regras em conflito"},
}

func parseGroupQuery(v url.Values) groupQuery {
	q := groupQuery{Tab: "maquinas", Q: texto.Clip(strings.TrimSpace(v.Get("q")), maxSearchLen),
		Building: texto.Clip(strings.TrimSpace(v.Get("predio")), 4*store.MaxBuildingLen), From: -1, Page: 1}
	if t := v.Get("aba"); slices.Contains(groupTabs, t) {
		q.Tab = t
	}
	if o := v.Get("como"); slices.ContainsFunc(originFilters, func(f struct{ V, L string }) bool { return f.V == o }) {
		q.Origin = o
	}
	if n, err := strconv.Atoi(v.Get("de")); err == nil && n >= 0 && n <= store.MaxRing {
		q.From = n
	}
	if n, _ := strconv.Atoi(v.Get("pagina")); n > 1 {
		q.Page = n
	}
	return q
}

// values: o filtro como URL (sem a aba), para links e campos ocultos.
func (q groupQuery) values() url.Values {
	v := url.Values{}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if q.Building != "" {
		v.Set("predio", q.Building)
	}
	if q.Origin != "" {
		v.Set("como", q.Origin)
	}
	if q.From >= 0 {
		v.Set("de", strconv.Itoa(q.From))
	}
	if q.Page > 1 {
		v.Set("pagina", strconv.Itoa(q.Page))
	}
	return v
}

func (q groupQuery) hasFilter() bool {
	return q.Q != "" || q.Building != "" || q.Origin != "" || q.From >= 0
}

// match: a máquina entra no filtro (o texto, ou a faixa de IP; o prédio exato).
func (q groupQuery) match(e equip, o origin) bool {
	if q.Building != "" && e.Asset.Building != q.Building {
		return false
	}
	switch q.Origin {
	case "":
	case "conflito":
		if o.Conflict == "" {
			return false
		}
	default:
		if o.Key != q.Origin {
			return false
		}
	}
	if q.From >= 0 && e.Ring != q.From {
		return false
	}
	if q.Q == "" {
		return true
	}
	if rng, ok := parseIPRange(q.Q); ok {
		return e.inRange(rng)
	}
	needle := strings.ToLower(q.Q)
	for _, s := range []string{e.Name, e.ID, e.IP, e.Asset.Tombo, e.Asset.Building, e.Asset.Room} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

// groupList: as máquinas da aba (no grupo, ou fora dele no adicionar), pelo filtro e por nome.
func (g groupFleet) groupList(ring int, q groupQuery) []equip {
	var out []equip
	for _, e := range g.all {
		inside := e.Ring == ring
		if inside != (q.Tab == "maquinas") {
			continue
		}
		if q.match(e, g.originOf(e)) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(x, y equip) int {
		return cmp.Or(cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.ID, y.ID))
	})
	return out
}

type groupMachineRow struct {
	ID, Name, Local, IP, OS, LastSeen, Group string
	Origin                                   origin
}

type ipRuleRow struct {
	ID                       int64
	Spec, Range, By, Updated string
	Machines, InRing, Pinned int
}

type groupView struct {
	pageBase
	Ring                          int
	Label, Name                   string
	Tab                           string
	Machines, Pinned, Conflicts   int
	Window, Next, WindowBy        string
	HasWindow                     bool
	Q, Building, Origin           string
	Buildings, Origins, Froms     []optView
	Targets                       []optView
	Rows                          []groupMachineRow
	Found                         int
	Shown, Prev, Next2, ClearHref string
	Hidden                        []optView // o filtro, para os formulários de lote
	HasFilter                     bool
	BuildingRows                  []buildingRingRow
	BuildingOpts                  []buildingOption
	IPRows                        []ipRuleRow
	HasAssets                     bool
	Days                          []struct{ Value, Label string }
	Durations                     []durationOption
	Timezone                      string
	MaxBatch, MaxPaste, MaxName   int
	TabHrefs                      map[string]string
	MachinesHref                  string
}

type buildingRingRow struct {
	Building, By, Updated    string
	Ring                     int
	Machines, InRing, Pinned int
	Missing                  bool // o prédio não está no patrimônio atual
}

type buildingOption struct {
	Name     string
	Machines int
	Ring     int // -1: sem vínculo
}

// groupPage: GET /painel/grupos-implantacao/{ring}.
func (a *api) groupPage(w http.ResponseWriter, r *http.Request) {
	ring, err := strconv.Atoi(r.PathValue("ring"))
	if err != nil || ring < 0 || ring > store.MaxRing {
		http.NotFound(w, r)
		return
	}
	q := parseGroupQuery(r.URL.Query())
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas do grupo", err)
		return
	}
	windows, err := a.store.Windows(r.Context())
	if err != nil {
		a.internalError(w, r, "listar janelas", err)
		return
	}
	v := groupView{pageBase: a.base(r, "Grupo de implantação "+strconv.Itoa(ring), "aneis"), Ring: ring,
		Label: groupLabel(ring, g.names), Name: g.names[ring], Tab: q.Tab, Q: q.Q, Building: q.Building, Origin: q.Origin,
		Days: dayValues, Timezone: a.tz(), MaxBatch: maxBatch, MaxPaste: maxPasteLines, MaxName: store.MaxRingNameLen,
		HasFilter: q.hasFilter(), TabHrefs: map[string]string{}}
	ringsFlash(r.URL.Query(), &v.pageBase)
	for _, t := range groupTabs {
		v.TabHrefs[t] = groupPath(ring, t, nil)
	}
	v.MachinesHref = v.TabHrefs["maquinas"]
	v.ClearHref = v.TabHrefs[q.Tab]
	for _, m := range windowDurations {
		v.Durations = append(v.Durations, durationOption{Minutes: m, Label: durationLabel(m)})
	}
	v.Window, v.Next, v.WindowBy, v.HasWindow = windowTexts(windows, ring, a.now())
	v.Targets = groupOptions(g.names, -1)
	buildings := map[string]bool{}
	for _, e := range g.all {
		if e.Asset.Building != "" {
			buildings[e.Asset.Building] = true
		}
		if e.Ring != ring {
			continue
		}
		v.Machines++
		if g.pins[e.ID] {
			v.Pinned++
		}
		if g.rules.decide(e).Conflict != "" {
			v.Conflicts++
		}
	}
	v.Buildings = []optView{{Value: "", Label: "Qualquer prédio"}}
	for _, b := range slices.Sorted(maps.Keys(buildings)) {
		v.Buildings = append(v.Buildings, optView{Value: b, Label: b, Selected: b == q.Building})
	}
	for _, o := range originFilters {
		v.Origins = append(v.Origins, optView{Value: o.V, Label: o.L, Selected: o.V == q.Origin})
	}
	v.Froms = []optView{{Value: "", Label: "De qualquer grupo"}}
	for rr := 0; rr <= store.MaxRing; rr++ {
		if rr != ring {
			v.Froms = append(v.Froms, optView{Value: strconv.Itoa(rr), Label: "Hoje no " + groupLabel(rr, g.names), Selected: rr == q.From})
		}
	}

	switch q.Tab {
	case "maquinas", "adicionar":
		list := g.groupList(ring, q)
		v.Found = len(list)
		page, pages := pageOf(strconv.Itoa(q.Page), len(list), groupPerPage)
		from, to := (page-1)*groupPerPage, min(page*groupPerPage, len(list))
		at := func(n int) string { c := q; c.Page = n; return groupPath(ring, q.Tab, c.values()) }
		if len(list) > 0 {
			v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(list))
		}
		if page > 1 {
			v.Prev = at(page - 1)
		}
		if page < pages {
			v.Next2 = at(page + 1)
		}
		now := a.now()
		for _, e := range list[from:to] {
			v.Rows = append(v.Rows, groupMachineRow{ID: e.ID, Name: dash(e.Name), Local: dash(e.localText()), IP: dash(e.IP), OS: dash(e.OS),
				LastSeen: ago(now.Sub(e.LastSeen)), Group: groupLabel(e.Ring, g.names), Origin: g.originOf(e)})
		}
		fq := q
		fq.Page = 1
		for k, vs := range fq.values() {
			v.Hidden = append(v.Hidden, optView{Value: k, Label: vs[0]})
		}
		slices.SortFunc(v.Hidden, func(x, y optView) int { return cmp.Compare(x.Value, y.Value) })
	case "regras":
		if err := a.fillGroupRules(r.Context(), &v, g); err != nil {
			a.internalError(w, r, "listar as regras do grupo", err)
			return
		}
	}
	a.render(w, r, http.StatusOK, "grupo", v)
}

// fillGroupRules: os prédios e as faixas do grupo, com as contagens.
func (a *api) fillGroupRules(ctx context.Context, v *groupView, g groupFleet) error {
	assets, err := a.assetMap(ctx)
	if err != nil {
		return err
	}
	v.HasAssets = len(assets) > 0
	links, err := a.store.BuildingRings(ctx)
	if err != nil {
		return err
	}
	inAssets := map[string]bool{}
	for _, l := range assets {
		if l.Building != "" {
			inAssets[l.Building] = true
		}
	}
	perBuilding := map[string][]equip{}
	for _, e := range g.all {
		if e.Asset.Building != "" {
			perBuilding[e.Asset.Building] = append(perBuilding[e.Asset.Building], e)
		}
	}
	now := a.now()
	linked := map[string]int{}
	for _, l := range links {
		linked[l.Building] = l.Ring
		if l.Ring != v.Ring {
			continue
		}
		row := buildingRingRow{Building: l.Building, Ring: l.Ring, By: l.UpdatedBy, Updated: ago(now.Sub(l.UpdatedAt)), Missing: !inAssets[l.Building]}
		for _, e := range perBuilding[l.Building] {
			row.Machines++
			switch {
			case e.Ring == l.Ring:
				row.InRing++
			case g.pins[e.ID]:
				row.Pinned++
			}
		}
		v.BuildingRows = append(v.BuildingRows, row)
	}
	for b := range inAssets {
		r, ok := linked[b]
		if ok && r == v.Ring {
			continue
		}
		if !ok {
			r = -1
		}
		v.BuildingOpts = append(v.BuildingOpts, buildingOption{Name: b, Machines: len(perBuilding[b]), Ring: r})
	}
	slices.SortFunc(v.BuildingOpts, func(x, y buildingOption) int {
		return cmp.Or(cmp.Compare(min(y.Machines, 1), min(x.Machines, 1)), cmp.Compare(x.Name, y.Name))
	})
	for _, ip := range g.rules.ips {
		if ip.Ring != v.Ring {
			continue
		}
		row := ipRuleRow{ID: ip.ID, Spec: ip.Spec, Range: ip.rng.lo.String() + " – " + ip.rng.hi.String(), By: ip.UpdatedBy,
			Updated: ago(now.Sub(ip.UpdatedAt))}
		for _, e := range g.all {
			if !e.inRange(ip.rng) {
				continue
			}
			row.Machines++
			switch {
			case e.Ring == ip.Ring:
				row.InRing++
			case g.pins[e.ID]:
				row.Pinned++
			}
		}
		v.IPRows = append(v.IPRows, row)
	}
	return nil
}
