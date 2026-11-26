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
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/window"
)

// Automação › Prédios e implantação (docs/implantacao-por-predio.md): os prédios do patrimônio
// como unidade de implantação. Cada prédio tem a sua janela de manutenção (vale para todos os
// andares dele), configurável um a um ou em lote; a lista começa recolhida e se filtra pelo
// nome e pela janela. Os grupos numéricos não aparecem aqui: continuam só para as campanhas
// legadas (as páginas /painel/grupos-implantacao/N seguem acessíveis por elas).

// deployFloor: um andar identificado de um prédio.
type deployFloor struct {
	Key, Label, Value string // Value: "ID|chave", o valor dos formulários
	Machines          int
}

// deployBuilding: um prédio, com a contagem e a janela.
type deployBuilding struct {
	ID                             int64
	Name, RenamedFrom              string
	Present                        bool
	Machines, Unfloored, Conflicts int
	Floors                         []deployFloor
	// A janela: HasWindow (existe), WindowOn (ativa), o texto, a próxima e quem gravou.
	HasWindow, WindowOn          bool
	WindowText, Next, WindowBy   string
	Win                          window.Window
	InvHref, LocHref, Anchor     string
	Start, End, TZ               string // o formulário da janela, preenchido
	Days                         []dayOpt
	Checked                      bool
	FloorsWithMachines           int
	WindowState                  string // "ok", "desativada", "sem"
	WindowClass, WindowStateText string
}

type dayOpt struct {
	Value, Label string
	Checked      bool
}

// deployOverview: o parque por prédio, para a página, o formulário de campanha e o plano.
type deployOverview struct {
	Buildings []deployBuilding
	byID      map[int64]int
	Unlocated int
	Total     int
	fleet     []locatedEquip
}

func (o *deployOverview) building(id int64) (*deployBuilding, bool) {
	i, ok := o.byID[id]
	if !ok {
		return nil, false
	}
	return &o.Buildings[i], true
}

// floor: o andar (chave) do prédio, se existe no prédio.
func (b *deployBuilding) floor(key string) (deployFloor, bool) {
	for _, f := range b.Floors {
		if f.Key == key {
			return f, true
		}
	}
	return deployFloor{}, false
}

func floorValue(id int64, key string) string { return strconv.FormatInt(id, 10) + "|" + key }

// parseFloorValue lê "ID|chave".
func parseFloorValue(v string) (int64, string, bool) {
	a, key, ok := strings.Cut(v, "|")
	id, err := strconv.ParseInt(a, 10, 64)
	if !ok || err != nil || id <= 0 || key == "" || len(key) > 300 || !utf8.ValidString(key) {
		return 0, "", false
	}
	return id, key, true
}

func windowNext(w window.Window, now time.Time) string {
	s, e := w.Next(now, window.MinDuration)
	loc := w.Location
	if s.IsZero() {
		return "—"
	}
	if !s.After(now) {
		return "aberta até " + e.In(loc).Format("15:04")
	}
	return s.In(loc).Format("02/01 15:04")
}

func (a *api) deployOverview(ctx context.Context) (*deployOverview, error) {
	fleet, l, err := a.fleetLocations(ctx)
	if err != nil {
		return nil, err
	}
	bs, err := a.store.Buildings(ctx)
	if err != nil {
		return nil, err
	}
	wins, err := a.store.BuildingWindows(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now()
	o := &deployOverview{byID: map[int64]int{}, fleet: fleet, Total: len(fleet)}
	type acc struct {
		floors map[string]*deployFloor
	}
	accs := map[int64]*acc{}
	count := map[int64]*deployBuilding{}
	for _, e := range fleet {
		if e.Loc.BuildingID == 0 {
			o.Unlocated++
			continue
		}
		b := count[e.Loc.BuildingID]
		if b == nil {
			b = &deployBuilding{}
			count[e.Loc.BuildingID] = b
			accs[e.Loc.BuildingID] = &acc{floors: map[string]*deployFloor{}}
		}
		b.Machines++
		switch {
		case e.Loc.Conflict:
			b.Conflicts++
		case e.Loc.FloorKey == "":
			b.Unfloored++
		default:
			f := accs[e.Loc.BuildingID].floors[e.Loc.FloorKey]
			if f == nil {
				f = &deployFloor{Key: e.Loc.FloorKey, Label: e.Loc.Floor}
				accs[e.Loc.BuildingID].floors[e.Loc.FloorKey] = f
			}
			f.Machines++
		}
	}
	// Os andares nomeados sem máquinas agora (para quem consulta; não servem de piloto).
	named := map[string]map[string]bool{}
	for k, n := range l.names {
		if n == "" {
			continue
		}
		if named[k.Building] == nil {
			named[k.Building] = map[string]bool{}
		}
		named[k.Building][n] = true
	}
	tz := a.panelLocation().String()
	for _, b := range bs {
		c := count[b.ID]
		w, hasW := wins[b.ID]
		if !b.Present && c == nil && !hasW {
			continue
		}
		d := deployBuilding{ID: b.ID, Name: b.Name, Present: b.Present, RenamedFrom: b.RenamedFrom,
			InvHref: "/painel/inventario?" + url.Values{"predio": {b.Name}}.Encode(),
			LocHref: "/painel/configuracao?aba=localizacoes",
			Anchor:  "predio-" + strconv.FormatInt(b.ID, 10),
			Start:   "22:00", End: "04:00", TZ: tz}
		if c != nil {
			d.Machines, d.Unfloored, d.Conflicts = c.Machines, c.Unfloored, c.Conflicts
		}
		floors := map[string]*deployFloor{}
		if ac := accs[b.ID]; ac != nil {
			floors = ac.floors
		}
		for n := range named[b.Name] {
			key := namedFloorPrefix + n
			if floors[key] == nil {
				floors[key] = &deployFloor{Key: key, Label: n}
			}
		}
		for _, f := range floors {
			f.Value = floorValue(b.ID, f.Key)
			d.Floors = append(d.Floors, *f)
			if f.Machines > 0 {
				d.FloorsWithMachines++
			}
		}
		slices.SortFunc(d.Floors, func(x, y deployFloor) int { return cmp.Compare(floorSortKey(x.Label), floorSortKey(y.Label)) })
		days := window.Days(0x1f) // seg-sex
		d.WindowState, d.WindowClass, d.WindowStateText = "sem", "location-stat-warn", "sem janela"
		if hasW {
			d.HasWindow, d.WindowOn, d.Win = true, w.Enabled, w.Window
			d.WindowText, d.WindowBy = w.Window.String(), w.UpdatedBy+" · "+ago(now.Sub(w.UpdatedAt))
			d.Start, d.End, d.TZ = clockText(w.Window.Start), clockText((w.Window.Start+w.Window.Duration)%(24*time.Hour)), w.Window.Location.String()
			days = w.Window.Days
			if w.Enabled {
				d.Next = windowNext(w.Window, now)
				d.WindowState, d.WindowClass, d.WindowStateText = "ok", "location-stat-ok", "janela ativa"
			} else {
				d.Next = "—"
				d.WindowState, d.WindowClass, d.WindowStateText = "desativada", "location-stat-warn", "janela desativada"
			}
		}
		for i, dv := range dayValues {
			d.Days = append(d.Days, dayOpt{Value: dv.Value, Label: dv.Label, Checked: days&(1<<i) != 0})
		}
		o.byID[b.ID] = len(o.Buildings)
		o.Buildings = append(o.Buildings, d)
	}
	slices.SortFunc(o.Buildings, func(x, y deployBuilding) int {
		return cmp.Or(cmp.Compare(strings.ToLower(texto.PlaceName(x.Name)), strings.ToLower(texto.PlaceName(y.Name))), cmp.Compare(x.ID, y.ID))
	})
	for i, b := range o.Buildings {
		o.byID[b.ID] = i
	}
	return o, nil
}

// floorSortKey: "Térreo" antes de "1º andar", "2º andar" antes de "10º andar".
func floorSortKey(label string) string {
	l := strings.ToLower(label)
	if strings.HasPrefix(l, "térreo") || strings.HasPrefix(l, "terreo") {
		return "0000 " + l
	}
	n := 0
	for _, r := range l {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	if n > 0 {
		return fmt.Sprintf("%04d %s", n, l)
	}
	return "9999 " + l
}

func clockText(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// --- A página ----------------------------------------------------------------------------

type deploymentView struct {
	pageBase
	Q, Filter           string
	Buildings           []deployBuilding
	NAll, NWith, NNoWin int
	Unlocated, Total    int
	Shown               int
	Legacy              int // prédios que podem receber a janela de um grupo antigo sem ambiguidade
	LegacyAmbiguous     int
	BatchHref           string
}

func deploymentFlash(q url.Values, b *pageBase) {
	n, _ := strconv.Atoi(q.Get("n"))
	switch {
	case q.Get("ok") == "janela":
		b.Flash = "Janela do prédio gravada. Vale para os jobs criados daqui em diante; os já criados mantêm a janela deles."
	case q.Get("ok") == "removida":
		b.Flash = "Janela do prédio removida. Campanhas \"na janela\" que chegarem a ele pausam até uma janela nova."
	case q.Get("ok") == "desativada":
		b.Flash = "Janela desativada: a configuração fica guardada, mas não vale para campanhas até ser ativada."
	case q.Get("ok") == "ativada":
		b.Flash = "Janela ativada."
	case q.Get("ok") == "lote":
		b.Flash = fmt.Sprintf("Janela aplicada a %d prédio(s).", max(n, 0))
	case q.Get("ok") == "lote-removida":
		b.Flash = fmt.Sprintf("Janela removida de %d prédio(s).", max(n, 0))
	case q.Get("ok") == "legadas":
		b.Flash = fmt.Sprintf("%d prédio(s) receberam a janela do grupo antigo.", max(n, 0))
	case q.Get("erro") == "predio":
		b.Error = "Prédio desconhecido."
	case q.Get("erro") == "janela":
		b.Error = "Nada foi gravado: " + shortError(q.Get("motivo"), 200)
	case q.Get("erro") == "lote-vazio":
		b.Error = "Marque ao menos um prédio."
	}
}

func filterBuildings(list []deployBuilding, q, filter string) []deployBuilding {
	needle := strings.ToLower(strings.TrimSpace(q))
	var out []deployBuilding
	for _, b := range list {
		if needle != "" && !strings.Contains(strings.ToLower(b.Name), needle) && !strings.Contains(strings.ToLower(texto.PlaceName(b.Name)), needle) {
			continue
		}
		switch filter {
		case "com":
			if b.WindowState != "ok" {
				continue
			}
		case "sem":
			if b.WindowState == "ok" {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// deploymentPage: GET /painel/grupos-implantacao.
func (a *api) deploymentPage(w http.ResponseWriter, r *http.Request) {
	v := deploymentView{pageBase: a.base(r, "Prédios e implantação", "aneis")}
	q := r.URL.Query()
	deploymentFlash(q, &v.pageBase)
	ringsFlash(q, &v.pageBase) // os avisos das páginas antigas dos grupos, que voltam para cá
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	v.Q, v.Filter = texto.Clip(strings.TrimSpace(q.Get("q")), 100), q.Get("janela")
	if v.Filter != "com" && v.Filter != "sem" {
		v.Filter = ""
	}
	v.NAll, v.Unlocated, v.Total = len(o.Buildings), o.Unlocated, o.Total
	for _, b := range o.Buildings {
		if b.WindowState == "ok" {
			v.NWith++
		} else {
			v.NNoWin++
		}
	}
	v.Buildings = filterBuildings(o.Buildings, v.Q, v.Filter)
	v.Shown = len(v.Buildings)
	v.BatchHref = "/painel/grupos-implantacao/janelas?" + url.Values{"q": {v.Q}, "janela": {v.Filter}}.Encode()
	if v.Admin {
		plan, err := a.legacyWindowPlan(r.Context(), o)
		if err != nil {
			a.internalError(w, r, "ler as janelas dos grupos antigos", err)
			return
		}
		v.Legacy, v.LegacyAmbiguous = len(plan.Apply), len(plan.Ambiguous)
	}
	a.render(w, r, http.StatusOK, "aneis", v)
}

// windowFromForm lê dias, início, término e fuso. O término antes do início passa da
// meia-noite (22:00 a 04:00); igual ao início é recusado.
func windowFromForm(r *http.Request) (window.Window, string) {
	days := r.PostForm["dia"]
	if len(days) == 0 || len(days) > 7 {
		return window.Window{}, "marque ao menos um dia da semana"
	}
	for _, d := range days {
		if !validDay(d) {
			return window.Window{}, "dia da semana inválido"
		}
	}
	d, err := window.ParseDays(strings.Join(days, ","))
	if err != nil {
		return window.Window{}, "dia da semana inválido"
	}
	start, err := window.ParseStart(r.PostFormValue("inicio"))
	if err != nil {
		return window.Window{}, "hora de início inválida (HH:MM)"
	}
	end, err := window.ParseStart(r.PostFormValue("fim"))
	if err != nil {
		return window.Window{}, "hora de término inválida (HH:MM)"
	}
	dur := end - start
	if dur < 0 {
		dur += 24 * time.Hour
	}
	if dur == 0 {
		return window.Window{}, "o término é igual ao início"
	}
	loc, err := loadZone(strings.TrimSpace(r.PostFormValue("fuso")))
	if err != nil {
		return window.Window{}, "fuso horário desconhecido (use um nome IANA, como America/Porto_Velho)"
	}
	win := window.Window{Days: d, Start: start, Duration: dur, Location: loc}
	if err := win.Validate(); err != nil {
		return window.Window{}, err.Error()
	}
	return win, ""
}

// buildingWindowAction: POST /painel/grupos-implantacao/predios/janela — gravar, remover,
// desativar ou ativar a janela de um prédio.
func (a *api) buildingWindowAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	_ = r.ParseForm()
	id, err := strconv.ParseInt(r.PostFormValue("predio"), 10, 64)
	o, oerr := a.deployOverview(r.Context())
	if oerr != nil {
		a.internalError(w, r, "listar os prédios", oerr)
		return
	}
	b, ok := o.building(id)
	if err != nil || !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=predio", http.StatusSeeOther)
		return
	}
	back := func(q url.Values) {
		http.Redirect(w, r, "/painel/grupos-implantacao?"+q.Encode()+"#"+b.Anchor, http.StatusSeeOther)
	}
	target := texto.PlaceName(b.Name)
	switch op := r.PostFormValue("op"); op {
	case "gravar":
		win, problem := windowFromForm(r)
		if problem != "" {
			back(url.Values{"erro": {"janela"}, "motivo": {problem}})
			return
		}
		if err := a.store.SetBuildingWindows(r.Context(), []int64{id}, win, s.Username); err != nil {
			a.internalError(w, r, "gravar a janela do prédio", err)
			return
		}
		before := "sem janela"
		if b.HasWindow {
			before = b.WindowText
		}
		a.audit(r, s, "janela.predio", target, fmt.Sprintf("prédio %d: %s → %s", id, before, win.String()))
		back(url.Values{"ok": {"janela"}})
	case "remover", "desativar", "ativar":
		var n int
		switch op {
		case "remover":
			n, err = a.store.DeleteBuildingWindows(r.Context(), []int64{id})
		default:
			n, err = a.store.SetBuildingWindowsEnabled(r.Context(), []int64{id}, op == "ativar", s.Username)
		}
		if err != nil {
			a.internalError(w, r, op+" a janela do prédio", err)
			return
		}
		if n > 0 {
			a.audit(r, s, "janela.predio-"+op, target, fmt.Sprintf("prédio %d: %s", id, b.WindowText))
		}
		back(url.Values{"ok": {map[string]string{"remover": "removida", "desativar": "desativada", "ativar": "ativada"}[op]}})
	default:
		http.Error(w, "operação desconhecida", http.StatusBadRequest)
	}
}

// --- Janelas em lote ---------------------------------------------------------------------

type windowBatchView struct {
	pageBase
	Q, Filter, Sel string
	Buildings      []deployBuilding
	Days           []dayOpt
	Start, End, TZ string
	// A confirmação: os prédios cuja janela será sobrescrita (ou removida).
	Confirm   bool
	Op        string
	Overwrite []batchOverwrite
	New       string
	Chosen    []deployBuilding
	Problem   string
	// Os atalhos de seleção (o filtro da tela fica).
	SelAll, SelNoWin, SelNone string
}

type batchOverwrite struct {
	Name, Before string
}

// windowBatchPage: GET /painel/grupos-implantacao/janelas?sel=todos|sem&q=&janela=.
func (a *api) windowBatchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := windowBatchView{pageBase: a.base(r, "Janelas em lote", "aneis"), Q: texto.Clip(strings.TrimSpace(q.Get("q")), 100), Filter: q.Get("janela"), Sel: q.Get("sel"),
		Start: "22:00", End: "04:00", TZ: a.panelLocation().String()}
	if v.Filter != "com" && v.Filter != "sem" {
		v.Filter = ""
	}
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	v.Buildings = filterBuildings(o.Buildings, v.Q, v.Filter)
	sel := func(x string) string {
		u := url.Values{"q": {v.Q}, "janela": {v.Filter}}
		if x != "" {
			u.Set("sel", x)
		}
		return "/painel/grupos-implantacao/janelas?" + u.Encode()
	}
	v.SelAll, v.SelNoWin, v.SelNone = sel("todos"), sel("sem"), sel("")
	for i := range v.Buildings {
		b := &v.Buildings[i]
		b.Checked = v.Sel == "todos" || (v.Sel == "sem" && b.WindowState != "ok")
	}
	for i, dv := range dayValues {
		v.Days = append(v.Days, dayOpt{Value: dv.Value, Label: dv.Label, Checked: i < 5})
	}
	a.render(w, r, http.StatusOK, "janelas-lote", v)
}

// windowBatchAction: POST /painel/grupos-implantacao/janelas. Sem "confirmar", e com alguma
// janela existente a sobrescrever (ou na remoção), mostra a confirmação antes.
func (a *api) windowBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	_ = r.ParseForm()
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	v := windowBatchView{pageBase: a.baseFor(r.Context(), s, "Janelas em lote", "aneis"), Op: r.PostFormValue("op"),
		Start: r.PostFormValue("inicio"), End: r.PostFormValue("fim"), TZ: r.PostFormValue("fuso")}
	seen := map[int64]bool{}
	var ids []int64
	for _, x := range r.PostForm["predio"] {
		id, err := strconv.ParseInt(x, 10, 64)
		b, ok := o.building(id)
		if err != nil || !ok {
			http.Error(w, "prédio desconhecido", http.StatusBadRequest)
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
			bb := *b
			bb.Checked = true
			v.Chosen = append(v.Chosen, bb)
		}
	}
	if len(ids) == 0 {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=lote-vazio", http.StatusSeeOther)
		return
	}
	if len(ids) > store.MaxWindowBatch {
		http.Error(w, "prédios demais num lote", http.StatusBadRequest)
		return
	}
	days := map[string]bool{}
	for _, d := range r.PostForm["dia"] {
		days[d] = true
	}
	for _, dv := range dayValues {
		v.Days = append(v.Days, dayOpt{Value: dv.Value, Label: dv.Label, Checked: days[dv.Value]})
	}
	var win window.Window
	switch v.Op {
	case "gravar":
		var problem string
		if win, problem = windowFromForm(r); problem != "" {
			v.Problem = problem
			v.Buildings = v.Chosen
			v.Error = "Nada foi gravado: " + problem + "."
			a.render(w, r, http.StatusUnprocessableEntity, "janelas-lote", v)
			return
		}
		v.New = win.String()
	case "remover":
		v.New = "sem janela"
	default:
		http.Error(w, "operação desconhecida", http.StatusBadRequest)
		return
	}
	for _, b := range v.Chosen {
		if b.HasWindow && (v.Op == "remover" || b.WindowText != v.New || !b.WindowOn) {
			before := b.WindowText
			if !b.WindowOn {
				before += " (desativada)"
			}
			v.Overwrite = append(v.Overwrite, batchOverwrite{Name: texto.PlaceName(b.Name), Before: before})
		}
	}
	if len(v.Overwrite) > 0 && r.PostFormValue("confirmar") != "1" {
		v.Confirm = true
		a.render(w, r, http.StatusOK, "janelas-lote-confirmar", v)
		return
	}
	names := make([]string, len(v.Chosen))
	for i, b := range v.Chosen {
		names[i] = fmt.Sprintf("%d %s", b.ID, texto.PlaceName(b.Name))
	}
	detail := texto.Clip(fmt.Sprintf("%d prédio(s), %d sobrescrita(s): %s → %s", len(ids), len(v.Overwrite), strings.Join(names, "; "), v.New), 2000)
	if v.Op == "remover" {
		n, err := a.store.DeleteBuildingWindows(r.Context(), ids)
		if err != nil {
			a.internalError(w, r, "remover as janelas", err)
			return
		}
		a.audit(r, s, "janela.lote-remover", fmt.Sprintf("%d prédio(s)", n), detail)
		http.Redirect(w, r, fmt.Sprintf("/painel/grupos-implantacao?ok=lote-removida&n=%d", n), http.StatusSeeOther)
		return
	}
	if err := a.store.SetBuildingWindows(r.Context(), ids, win, s.Username); err != nil {
		a.internalError(w, r, "gravar as janelas", err)
		return
	}
	a.audit(r, s, "janela.lote", fmt.Sprintf("%d prédio(s)", len(ids)), detail)
	http.Redirect(w, r, fmt.Sprintf("/painel/grupos-implantacao?ok=lote&n=%d", len(ids)), http.StatusSeeOther)
}

// --- Janelas dos grupos antigos ----------------------------------------------------------

// legacyPlan: os prédios sem janela que podem herdar, sem dúvida, a janela do grupo antigo
// (todas as máquinas do prédio e o vínculo do prédio apontam para grupos com a mesma janela), e
// os ambíguos, que ficam para o admin.
type legacyPlan struct {
	Apply     []legacyApply
	Ambiguous []legacyAmbiguous
}

type legacyApply struct {
	ID           int64
	Name, Groups string
	Window       window.Window
	WindowText   string
}

type legacyAmbiguous struct {
	Name, Reason string
}

func (a *api) legacyWindowPlan(ctx context.Context, o *deployOverview) (legacyPlan, error) {
	var plan legacyPlan
	rws, err := a.store.Windows(ctx)
	if err != nil {
		return plan, err
	}
	links, err := a.store.BuildingRings(ctx)
	if err != nil {
		return plan, err
	}
	linked := map[string]int{}
	for _, l := range links {
		linked[l.Building] = l.Ring
	}
	rings := map[int64]map[int]bool{}
	for _, e := range o.fleet {
		if e.Loc.BuildingID == 0 {
			continue
		}
		if rings[e.Loc.BuildingID] == nil {
			rings[e.Loc.BuildingID] = map[int]bool{}
		}
		rings[e.Loc.BuildingID][e.Ring] = true
	}
	for _, b := range o.Buildings {
		if b.HasWindow {
			continue // nunca sobrescreve a janela do prédio
		}
		set := map[int]bool{}
		maps.Copy(set, rings[b.ID])
		if r, ok := linked[b.Name]; ok {
			set[r] = true
		}
		if len(set) == 0 {
			continue
		}
		wins := map[string]window.Window{}
		var without []string
		for r := range set {
			if rw, ok := store.WindowFor(rws, r); ok {
				wins[rw.Window.String()] = rw.Window
			} else {
				without = append(without, strconv.Itoa(r))
			}
		}
		groups := make([]string, 0, len(set))
		for _, r := range slices.Sorted(maps.Keys(set)) {
			groups = append(groups, strconv.Itoa(r))
		}
		switch {
		case len(wins) == 0:
		case len(wins) == 1 && len(without) == 0:
			for k, w := range wins {
				plan.Apply = append(plan.Apply, legacyApply{ID: b.ID, Name: b.Name, Groups: strings.Join(groups, ", "), Window: w, WindowText: k})
			}
		case len(wins) > 1:
			plan.Ambiguous = append(plan.Ambiguous, legacyAmbiguous{Name: b.Name, Reason: fmt.Sprintf("máquinas em grupos com janelas diferentes (grupos %s)", strings.Join(groups, ", "))})
		default:
			slices.Sort(without)
			plan.Ambiguous = append(plan.Ambiguous, legacyAmbiguous{Name: b.Name, Reason: fmt.Sprintf("parte das máquinas em grupo sem janela (grupos %s; sem janela: %s)", strings.Join(groups, ", "), strings.Join(without, ", "))})
		}
	}
	return plan, nil
}

type legacyView struct {
	pageBase
	Plan legacyPlan
}

// legacyWindowsPage: GET /painel/grupos-implantacao/janelas-legadas.
func (a *api) legacyWindowsPage(w http.ResponseWriter, r *http.Request) {
	v := legacyView{pageBase: a.base(r, "Janelas dos grupos antigos", "aneis")}
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	if v.Plan, err = a.legacyWindowPlan(r.Context(), o); err != nil {
		a.internalError(w, r, "ler as janelas dos grupos antigos", err)
		return
	}
	a.render(w, r, http.StatusOK, "janelas-legadas", v)
}

// legacyWindowsAction: POST /painel/grupos-implantacao/janelas-legadas. Recalcula o plano (o
// navegador não escolhe nada) e aplica só os sem ambiguidade e ainda sem janela.
func (a *api) legacyWindowsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	o, err := a.deployOverview(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os prédios", err)
		return
	}
	plan, err := a.legacyWindowPlan(r.Context(), o)
	if err != nil {
		a.internalError(w, r, "ler as janelas dos grupos antigos", err)
		return
	}
	n := 0
	for _, p := range plan.Apply {
		if err := a.store.SetBuildingWindows(r.Context(), []int64{p.ID}, p.Window, s.Username); err != nil {
			a.internalError(w, r, "gravar a janela do prédio", err)
			return
		}
		a.audit(r, s, "janela.predio-legada", texto.PlaceName(p.Name), fmt.Sprintf("prédio %d: janela dos grupos %s → %s", p.ID, p.Groups, p.WindowText))
		n++
	}
	http.Redirect(w, r, fmt.Sprintf("/painel/grupos-implantacao?ok=legadas&n=%d", n), http.StatusSeeOther)
}
