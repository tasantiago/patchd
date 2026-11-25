package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
)

// Atende+ Shield no painel (docs/shield.md): Shield › Visão geral, Equipamentos, Pessoas,
// Missões, Ranking, Exceções, Política e Auditoria. Ler é de qualquer perfil do painel;
// mudar é do admin, com a conferência de origem (CSRF) e a auditoria. O ranking nunca mostra
// máquina, CVE ou programa: só grupos (e, se ligado, nome e números da pessoa).

const shieldPerPage = 100

var shieldTabs = []struct{ ID, Label string }{
	{"geral", "Visão geral"}, {"equipamentos", "Equipamentos"}, {"pessoas", "Pessoas"}, {"missoes", "Missões"},
	{"ranking", "Ranking"}, {"excecoes", "Exceções"}, {"politica", "Política"}, {"auditoria", "Auditoria"},
}

type shieldTab struct {
	ID, Label string
	Active    bool
}

type shieldCount struct {
	Label, Class, Href string
	N                  int
	Active             bool
}

type shieldTrendRow struct {
	Day              string
	Machines, Sealed int
	Pct              int
	Avg              string
}

type shieldMachineRow struct {
	ID, Name, Ring, Overall, OverallLabel, Blame, Blockers, Changed, Evaluated string
	Display                                                                    int
	Sealed                                                                     bool
}

type shieldPlayerRow struct {
	ID                      int64
	Account, Name, Level    string
	XP                      int64
	Streak, Best, Protected int
	Seen                    string
}

type shieldMissionRow struct {
	ID                                        int64
	Title, Description, Scope, Goal, Scope2   string
	Status, StatusLabel, Period, Progress, By string
	XP, Percent                               int
	CanPublish, CanCancel                     bool
}

type shieldGroupRow struct {
	Pos              int
	Name             string
	Machines, Sealed int
	Pct              int
	Avg              string
}

type shieldLeaderRow struct {
	Pos, Protected, Streak int
	Name                   string
	XP                     int64
}

type shieldPlaceRow struct {
	Pos, Overall, Machines, Sealed, Pct int
	Label, Spec, Avg, Custom            string
}

type shieldBuildingPlaces struct {
	Building string
	Floors   []shieldPlaceRow
	Rooms    []shieldPlaceRow
}

type shieldExcRow struct {
	ID                                                  int64
	MachineID, Host, Criterion, Reason, Expires, By, At string
	Revoked                                             string
	Active                                              bool
}

type shieldCritRow struct {
	ID, Label                    string
	Enabled, Mandatory           bool
	Weight                       string
	Grace                        int
	Conforme, Fora, Unknown, Pct int
}

type shieldRingOpt struct {
	N       int
	Label   string
	Checked bool
}

type shieldDayOpt struct {
	N       int
	Label   string
	Checked bool
}

type shieldVersionRow struct {
	Version int
	By, At  string
	Note    string
}

type shieldAuditRow struct {
	At, User, Action, Target, Detail string
}

type shieldPanelView struct {
	pageBase
	Tab     string
	Tabs    []shieldTab
	On      bool // há PostgreSQL
	Pol     shield.Policy
	Version int
	PolBy   string
	PolAt   string
	Scope   string // o alcance (todos, ou os grupos piloto)
	LastRun string
	LastErr string
	// Visão geral.
	Total, Sealed int
	SealedPct     int
	Average       string
	Counts        []shieldCount
	Blames        []shieldCount
	Crit          []shieldCritRow
	Trend         []shieldTrendRow
	// Equipamentos.
	Machines           []shieldMachineRow
	Filter, Q          string
	Page, Pages, Hit   int
	PrevHref, NextHref string
	// Pessoas.
	Players []shieldPlayerRow
	Ref     string // a chave do formulário de ajuste (o reenvio não soma duas vezes)
	// Missões.
	Missions []shieldMissionRow
	// Ranking.
	Groups  []shieldGroupRow
	Hidden  int
	Leaders []shieldLeaderRow
	// Rankings por lugar: os prédios e, por prédio, os andares (faixas) e as salas.
	PlaceB []shieldPlaceRow
	PlaceP []shieldBuildingPlaces
	// Exceções.
	Exceptions []shieldExcRow
	AllExc     bool
	// Política.
	Rings        []shieldRingOpt
	Days         []shieldDayOpt
	CritForm     []shieldCritRow
	Levels       string
	Achievements string
	Holidays     string
	Bonus        string
	Credits      map[string]string
	History      []shieldVersionRow
	// Auditoria.
	Audit    []shieldAuditRow
	Criteria []shield.Criterion
}

// shieldPage: GET /painel/shield.
func (a *api) shieldPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := shieldPanelView{pageBase: a.base(r, "Shield", "shield"), Tab: q.Get("aba"), Criteria: shield.Criteria}
	if !slices.ContainsFunc(shieldTabs, func(t struct{ ID, Label string }) bool { return t.ID == v.Tab }) {
		v.Tab = "geral"
	}
	for _, t := range shieldTabs {
		v.Tabs = append(v.Tabs, shieldTab{ID: t.ID, Label: t.Label, Active: t.ID == v.Tab})
	}
	v.Sub = v.Tab
	shieldFlash(q, &v.pageBase)
	db, ok := a.shieldDB()
	v.On = ok
	if !ok {
		a.render(w, r, http.StatusOK, "shield", v)
		return
	}
	ctx := r.Context()
	pol, ver, err := a.shieldPolicy(ctx)
	if err != nil {
		a.internalError(w, r, "ler a política do Shield", err)
		return
	}
	v.Pol, v.Version = pol, ver
	if row, found, err := db.ShieldPolicy(ctx); err == nil && found {
		v.PolBy, v.PolAt = row.CreatedBy, a.stamp(row.CreatedAt)
	}
	names, err := a.ringNames(ctx)
	if err != nil {
		a.internalError(w, r, "ler os grupos", err)
		return
	}
	v.Scope = "todos os grupos de implantação"
	if len(pol.PilotRings) > 0 {
		var l []string
		for _, n := range pol.PilotRings {
			l = append(l, groupLabel(n, names))
		}
		v.Scope = "piloto: " + strings.Join(l, ", ")
	}
	a.shield.mu.Lock()
	if !a.shield.lastRun.IsZero() {
		v.LastRun = a.stamp(a.shield.lastRun) + fmt.Sprintf(" (%d ms, neste servidor)", a.shield.duration.Milliseconds())
	}
	v.LastErr = a.shield.lastErr
	a.shield.mu.Unlock()
	var fill func() error
	switch v.Tab {
	case "geral":
		fill = func() error { return a.shieldGeneral(ctx, db, &v) }
	case "equipamentos":
		fill = func() error { return a.shieldMachinesTab(ctx, db, &v, q, names) }
	case "pessoas":
		fill = func() error { return a.shieldPlayersTab(ctx, db, &v, q) }
	case "missoes":
		fill = func() error { return a.shieldMissionsTab(ctx, db, &v, names) }
	case "ranking":
		fill = func() error { return a.shieldRankingTab(ctx, db, &v, names) }
	case "excecoes":
		fill = func() error { return a.shieldExceptionsTab(ctx, db, &v, q) }
	case "politica":
		fill = func() error { return a.shieldPolicyTab(ctx, db, &v, names) }
	case "auditoria":
		fill = func() error { return a.shieldAuditTab(ctx, &v) }
	}
	if err := fill(); err != nil {
		a.internalError(w, r, "montar o Shield", err)
		return
	}
	a.render(w, r, http.StatusOK, "shield", v)
}

// shieldFlash: as mensagens fixas da volta das ações.
func shieldFlash(q url.Values, b *pageBase) {
	switch {
	case q.Has("politica"):
		b.Flash = "Política gravada como versão " + numOnly(q.Get("politica")) + ". Vale na próxima avaliação (em até 5 minutos)."
	case q.Has("missao"):
		b.Flash = "Missão gravada."
	case q.Has("estado"):
		b.Flash = "Situação da missão alterada."
	case q.Has("excecao"):
		b.Flash = "Exceção registrada. Vale na próxima avaliação (em até 5 minutos)."
	case q.Has("revogada"):
		b.Flash = "Exceção revogada."
	case q.Get("andares") == "0":
		b.Flash = "Nenhum nome de andar mudou."
	case q.Has("andares"):
		b.Flash = "Nomes dos andares gravados (" + numOnly(q.Get("andares")) + "). Aparecem na próxima avaliação (em até 5 minutos)."
	case q.Has("ajuste"):
		b.Flash = "Ajuste de XP registrado no livro do XP e na auditoria."
	case q.Has("repetido"):
		b.Flash = "Esse formulário já tinha sido enviado: nada mudou."
	case q.Get("erro") != "":
		b.Error = "Nada foi gravado: " + shortError(q.Get("motivo"), 300) + "."
	}
}

// overallClass: a cor de cada faixa (sempre com o texto junto).
func overallClass(o shield.Overall) string {
	switch o {
	case shield.Protected:
		return "v-verde"
	case shield.Near:
		return "v-azul"
	case shield.Attention:
		return "v-laranja"
	case shield.Risk:
		return "v-vermelho"
	}
	return "v-cinza"
}

func (a *api) shieldGeneral(ctx context.Context, db shieldStore, v *shieldPanelView) error {
	scores, err := db.ShieldScores(ctx)
	if err != nil {
		return err
	}
	counts := map[shield.Overall]int{}
	blames := map[shield.Blame]int{}
	crit := map[shield.Criterion]*shieldCritRow{}
	sum := 0.0
	for _, s := range scores {
		v.Total++
		counts[s.Result.Overall]++
		if s.Result.Sealed {
			v.Sealed++
		} else {
			blames[s.Result.Blame]++
		}
		if s.Result.Score != nil {
			sum += *s.Result.Score
		}
		for _, c := range s.Result.Criteria {
			row := crit[c.Criterion]
			if row == nil {
				row = &shieldCritRow{ID: string(c.Criterion), Label: c.Criterion.Label()}
				crit[c.Criterion] = row
			}
			switch c.Status {
			case shield.Conforme:
				row.Conforme++
			case shield.Desconhecido:
				row.Unknown++
			case shield.NaoAplicavel, shield.Excecao:
			default:
				row.Fora++
			}
		}
	}
	if v.Total > 0 {
		v.SealedPct = v.Sealed * 100 / v.Total
		v.Average = pct1(math.Floor(sum/float64(v.Total)*10) / 10)
	}
	for _, o := range []shield.Overall{shield.Protected, shield.Near, shield.Attention, shield.Risk, shield.NoData} {
		v.Counts = append(v.Counts, shieldCount{Label: o.Label(), N: counts[o], Class: overallClass(o), Href: "/painel/shield?aba=equipamentos&faixa=" + string(o)})
	}
	for _, b := range []shield.Blame{shield.BlameUser, shield.BlameIT, shield.BlameAuto, shield.BlameExternal, shield.BlameUnknown} {
		if blames[b] > 0 {
			v.Blames = append(v.Blames, shieldCount{Label: b.Label(), N: blames[b]})
		}
	}
	for _, c := range shield.Criteria {
		if row := crit[c]; row != nil {
			if n := row.Conforme + row.Fora + row.Unknown; n > 0 {
				row.Pct = row.Conforme * 100 / n
			}
			v.Crit = append(v.Crit, *row)
		}
	}
	trend, err := db.ShieldTrends(ctx, a.now().AddDate(0, 0, -30))
	if err != nil {
		return err
	}
	for _, t := range trend {
		row := shieldTrendRow{Day: t.Day.Format("02/01"), Machines: t.Machines, Sealed: t.Sealed, Avg: pct1(t.Average)}
		if t.Machines > 0 {
			row.Pct = t.Sealed * 100 / t.Machines
		}
		v.Trend = append(v.Trend, row)
	}
	return nil
}

func (a *api) shieldMachinesTab(ctx context.Context, db shieldStore, v *shieldPanelView, q url.Values, rings map[int]string) error {
	rows, err := a.shieldMachineRows(ctx, db, rings)
	if err != nil {
		return err
	}
	v.Filter, v.Q = q.Get("faixa"), strings.TrimSpace(q.Get("q"))
	if len(v.Q) > 100 {
		v.Q = v.Q[:100]
	}
	kept := rows[:0]
	for _, m := range rows {
		if v.Filter != "" && m.Overall != v.Filter {
			continue
		}
		if v.Q != "" && !ssMatches(v.Q, m.Name, m.ID, m.Ring) {
			continue
		}
		kept = append(kept, m)
	}
	v.Hit = len(kept)
	v.Page, _ = strconv.Atoi(q.Get("pagina"))
	v.Pages = max(1, (len(kept)+shieldPerPage-1)/shieldPerPage)
	v.Page = min(max(v.Page, 1), v.Pages)
	from := (v.Page - 1) * shieldPerPage
	v.Machines = kept[from:min(from+shieldPerPage, len(kept))]
	link := func(p int) string {
		u := url.Values{"aba": {"equipamentos"}, "pagina": {strconv.Itoa(p)}}
		if v.Filter != "" {
			u.Set("faixa", v.Filter)
		}
		if v.Q != "" {
			u.Set("q", v.Q)
		}
		return "/painel/shield?" + u.Encode()
	}
	if v.Page > 1 {
		v.PrevHref = link(v.Page - 1)
	}
	if v.Page < v.Pages {
		v.NextHref = link(v.Page + 1)
	}
	for _, o := range []shield.Overall{shield.Protected, shield.Near, shield.Attention, shield.Risk, shield.NoData} {
		v.Counts = append(v.Counts, shieldCount{Label: o.Label(), Active: v.Filter == string(o), Href: "/painel/shield?aba=equipamentos&faixa=" + string(o)})
	}
	return nil
}

// shieldMachineRows: as máquinas com pontuação, da menor para a maior (o que pede ação primeiro).
func (a *api) shieldMachineRows(ctx context.Context, db shieldStore, rings map[int]string) ([]shieldMachineRow, error) {
	scores, err := db.ShieldScores(ctx)
	if err != nil {
		return nil, err
	}
	machines, err := db.ShieldMachines(ctx)
	if err != nil {
		return nil, err
	}
	info := map[string]store.ShieldMachine{}
	for _, m := range machines {
		info[m.ID] = m
	}
	var out []shieldMachineRow
	for id, s := range scores {
		m := info[id]
		row := shieldMachineRow{ID: id, Name: m.Hostname, Ring: groupLabel(m.Ring, rings), Display: s.Result.Display, Sealed: s.Result.Sealed,
			Overall: string(s.Result.Overall), OverallLabel: s.Result.Overall.Label(), Changed: a.stamp(s.ChangedAt), Evaluated: a.stamp(s.EvaluatedAt)}
		if row.Name == "" {
			row.Name = short(id)
		}
		if s.Result.Blame != shield.BlameNone {
			row.Blame = s.Result.Blame.Label()
		}
		row.Blockers = strings.Join(s.Result.Blockers, "; ")
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Display != out[j].Display {
			return out[i].Display < out[j].Display
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (a *api) shieldPlayersTab(ctx context.Context, db shieldStore, v *shieldPanelView, q url.Values) error {
	v.Q = strings.TrimSpace(q.Get("q"))
	if len(v.Q) > 100 {
		v.Q = v.Q[:100]
	}
	v.Page, _ = strconv.Atoi(q.Get("pagina"))
	v.Page = max(v.Page, 1)
	list, total, err := db.ShieldPlayerList(ctx, v.Q, shieldPerPage, (v.Page-1)*shieldPerPage)
	if err != nil {
		return err
	}
	v.Hit, v.Pages = total, max(1, (total+shieldPerPage-1)/shieldPerPage)
	for _, p := range list {
		row := shieldPlayerRow{ID: p.ID, Account: p.Account, Name: p.DisplayName, XP: p.XP, Streak: p.Streak.Current, Best: p.Streak.Best,
			Protected: p.Streak.Protected, Seen: a.stamp(p.SeenAt)}
		if cur, _ := v.Pol.LevelFor(p.Streak.Protected); cur >= 0 {
			row.Level = v.Pol.Levels[cur].Name
		}
		v.Players = append(v.Players, row)
	}
	link := func(p int) string {
		u := url.Values{"aba": {"pessoas"}, "pagina": {strconv.Itoa(p)}}
		if v.Q != "" {
			u.Set("q", v.Q)
		}
		return "/painel/shield?" + u.Encode()
	}
	if v.Page > 1 {
		v.PrevHref = link(v.Page - 1)
	}
	if v.Page < v.Pages {
		v.NextHref = link(v.Page + 1)
	}
	v.Ref = newShieldRef()
	return nil
}

func newShieldRef() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func missionStatusLabel(s string) string {
	switch s {
	case "rascunho":
		return "rascunho"
	case "agendada":
		return "agendada"
	case "ativa":
		return "ativa"
	case "concluida":
		return "cumprida"
	case "encerrada":
		return "encerrada"
	case "cancelada":
		return "cancelada"
	}
	return s
}

func (a *api) shieldMissionsTab(ctx context.Context, db shieldStore, v *shieldPanelView, rings map[int]string) error {
	ms, err := db.ShieldMissions(ctx, 200)
	if err != nil {
		return err
	}
	now := a.now()
	loc := v.Pol.Location(a.panelLocation())
	for _, m := range ms {
		st := missionStatus(m, now)
		row := shieldMissionRow{ID: m.ID, Title: m.Title, Description: m.Description, Scope: m.Scope, Status: st, StatusLabel: missionStatusLabel(st),
			XP: m.XP, By: m.CreatedBy, Period: m.StartsAt.In(loc).Format("02/01/2006") + " a " + m.EndsAt.In(loc).Format("02/01/2006"),
			CanPublish: m.State == "rascunho", CanCancel: m.State != "cancelada" && st != "encerrada" && st != "concluida"}
		row.Scope2 = "todo o Shield"
		if m.Ring != nil {
			row.Scope2 = groupLabel(*m.Ring, rings)
		}
		switch m.Scope {
		case "individual":
			row.Goal = fmt.Sprintf("%d dia(s) protegido(s) no período", m.Goal.Days)
		default:
			what := "no selo 100%"
			if m.Goal.Criterion != "" {
				what = "com “" + m.Goal.Criterion.Label() + "” conforme"
			}
			row.Goal = fmt.Sprintf("%d%% das máquinas %s", m.Goal.Percent, what)
			if m.Total > 0 {
				row.Percent = min(100, m.Done*100/m.Total)
				row.Progress = fmt.Sprintf("%d de %d máquinas (%d%%)", m.Done, m.Total, row.Percent)
			}
		}
		v.Missions = append(v.Missions, row)
	}
	v.Rings = a.shieldRingOpts(rings, nil)
	return nil
}

func (a *api) shieldRingOpts(names map[int]string, checked []int) []shieldRingOpt {
	var out []shieldRingOpt
	for n := 0; n <= 9; n++ {
		out = append(out, shieldRingOpt{N: n, Label: groupLabel(n, names), Checked: slices.Contains(checked, n)})
	}
	return out
}

// shieldGroups: o ranking coletivo (grupos do Shield com o mínimo de máquinas), do melhor para o pior.
func shieldGroups(pol *shield.Policy, stats []store.ShieldRingStat, rings map[int]string) (out []shieldGroupRow, hidden int) {
	for _, s := range stats {
		if !pol.InScope(s.Ring) {
			continue
		}
		if s.Machines < pol.Ranking.MinGroup {
			hidden++
			continue
		}
		out = append(out, shieldGroupRow{Name: groupLabel(s.Ring, rings), Machines: s.Machines, Sealed: s.Sealed, Pct: s.Sealed * 100 / s.Machines,
			Avg: pct1(math.Floor(s.Average*10) / 10)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pct != out[j].Pct {
			return out[i].Pct > out[j].Pct
		}
		return out[i].Avg > out[j].Avg
	})
	for i := range out {
		out[i].Pos = i + 1
	}
	return out, hidden
}

func (a *api) shieldRankingTab(ctx context.Context, db shieldStore, v *shieldPanelView, rings map[int]string) error {
	if v.Pol.Ranking.Collective {
		stats, err := db.ShieldRingStats(ctx)
		if err != nil {
			return err
		}
		v.Groups, v.Hidden = shieldGroups(&v.Pol, stats, rings)
	}
	if v.Pol.Ranking.Individual {
		l, err := db.ShieldLeaders(ctx, a.now().AddDate(0, 0, -30), 20)
		if err != nil {
			return err
		}
		for i, x := range l {
			name := x.DisplayName
			if name == "" {
				name = x.Account
			}
			v.Leaders = append(v.Leaders, shieldLeaderRow{Pos: i + 1, Name: name, XP: x.XP, Protected: x.Protected, Streak: x.Streak})
		}
	}
	row := func(r store.ShieldRank) shieldPlaceRow {
		x := shieldPlaceRow{Pos: r.Pos, Overall: r.Overall, Machines: r.Machines, Sealed: r.Sealed, Pct: r.Percent(), Label: r.Label, Avg: pct1(r.Average)}
		if r.Kind == shield.RankFloor {
			x.Spec, x.Custom = r.Name, v.Pol.Ranking.FloorNames[r.Name]
		}
		return x
	}
	buildings, err := db.ShieldRankings(ctx, shield.RankBuilding, "", 500)
	if err != nil {
		return err
	}
	byB := map[string]*shieldBuildingPlaces{}
	for _, b := range buildings {
		v.PlaceB = append(v.PlaceB, row(b))
		v.PlaceP = append(v.PlaceP, shieldBuildingPlaces{Building: b.Building})
	}
	for i := range v.PlaceP {
		byB[v.PlaceP[i].Building] = &v.PlaceP[i]
	}
	for _, kind := range []string{shield.RankFloor, shield.RankRoom} {
		list, err := db.ShieldRankings(ctx, kind, "", 5000)
		if err != nil {
			return err
		}
		for _, r := range list {
			bp := byB[r.Building]
			if bp == nil { // o prédio é pequeno demais para o ranking, mas os andares ou as salas não
				v.PlaceP = append(v.PlaceP, shieldBuildingPlaces{Building: r.Building})
				bp = &v.PlaceP[len(v.PlaceP)-1]
				byB = map[string]*shieldBuildingPlaces{}
				for i := range v.PlaceP {
					byB[v.PlaceP[i].Building] = &v.PlaceP[i]
				}
			}
			if kind == shield.RankFloor {
				bp.Floors = append(bp.Floors, row(r))
			} else {
				bp.Rooms = append(bp.Rooms, row(r))
			}
		}
	}
	kept := v.PlaceP[:0]
	for _, bp := range v.PlaceP {
		if len(bp.Floors) > 0 || len(bp.Rooms) > 0 {
			kept = append(kept, bp)
		}
	}
	v.PlaceP = kept
	return nil
}

var errFloorName = errors.New("nome de andar: de 1 a 40 caracteres, sem caracteres de controle")

// shieldFloorsAction: POST /painel/shield/andares — os nomes das faixas de IP como andares
// (uma versão nova da política, auditada). Nome vazio volta ao "Faixa N".
func (a *api) shieldFloorsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "ranking", errShieldOff)
		return
	}
	if err := r.ParseForm(); err != nil {
		shieldFail(w, r, "ranking", errShieldForm)
		return
	}
	cur, _, err := a.shieldPolicy(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a política do Shield", err)
		return
	}
	specs, names := r.PostForm["faixa"], r.PostForm["nome"]
	if len(specs) != len(names) || len(specs) == 0 || len(specs) > 500 {
		shieldFail(w, r, "ranking", errShieldForm)
		return
	}
	pol := cur
	pol.Ranking.FloorNames = map[string]string{}
	for k, n := range cur.Ranking.FloorNames {
		pol.Ranking.FloorNames[k] = n
	}
	var changed []string
	for i, spec := range specs {
		spec = strings.TrimSpace(spec)
		name := strings.TrimSpace(names[i])
		if spec == "" || len(spec) > 64 {
			shieldFail(w, r, "ranking", errShieldForm)
			return
		}
		if pol.Ranking.FloorNames[spec] == name {
			continue
		}
		if name == "" {
			delete(pol.Ranking.FloorNames, spec)
		} else {
			if len([]rune(name)) > 40 {
				shieldFail(w, r, "ranking", errFloorName)
				return
			}
			pol.Ranking.FloorNames[spec] = name
		}
		changed = append(changed, fmt.Sprintf("%s=%q", spec, name))
	}
	if len(changed) == 0 {
		shieldBack(w, r, "ranking", "andares=0")
		return
	}
	if err := pol.Validate(); err != nil {
		shieldFail(w, r, "ranking", err)
		return
	}
	ver, err := db.SaveShieldPolicy(r.Context(), pol, "nomes dos andares", s.Username)
	if err != nil {
		shieldFail(w, r, "ranking", err)
		return
	}
	a.shieldPolicyStale()
	a.audit(r, s, "shield.andares", fmt.Sprintf("versão %d", ver), strings.Join(changed, "; "))
	shieldBack(w, r, "ranking", "andares="+strconv.Itoa(len(changed)))
}

func (a *api) shieldExceptionsTab(ctx context.Context, db shieldStore, v *shieldPanelView, q url.Values) error {
	v.AllExc = q.Get("todas") == "1"
	now := a.now()
	list, err := db.ShieldExceptions(ctx, v.AllExc, now)
	if err != nil {
		return err
	}
	for _, e := range list {
		row := shieldExcRow{ID: e.ID, MachineID: e.MachineID, Host: e.Hostname, Criterion: e.Criterion.Label(), Reason: e.Reason,
			Expires: a.stamp(e.ExpiresAt), By: e.CreatedBy, At: a.stamp(e.CreatedAt), Active: e.RevokedAt == nil && e.ExpiresAt.After(now)}
		if row.Host == "" {
			row.Host = short(e.MachineID)
		}
		if e.RevokedAt != nil {
			row.Revoked = a.stamp(*e.RevokedAt) + " por " + e.RevokedBy
		}
		v.Exceptions = append(v.Exceptions, row)
	}
	return nil
}

var weekdayNames = []string{"domingo", "segunda", "terça", "quarta", "quinta", "sexta", "sábado"}

// pct1: a porcentagem com uma casa e vírgula ("86,1%").
func pct1(f float64) string { return strings.Replace(fmt.Sprintf("%.1f%%", f), ".", ",", 1) }

func fmtFloat(f float64) string {
	return strings.Replace(strconv.FormatFloat(f, 'f', -1, 64), ".", ",", 1)
}

func (a *api) shieldPolicyTab(ctx context.Context, db shieldStore, v *shieldPanelView, rings map[int]string) error {
	p := v.Pol
	v.Rings = a.shieldRingOpts(rings, p.PilotRings)
	for i, n := range weekdayNames {
		v.Days = append(v.Days, shieldDayOpt{N: i, Label: n, Checked: slices.Contains(p.Workdays, i)})
	}
	for _, c := range shield.Criteria {
		cp := p.Criteria[c]
		v.CritForm = append(v.CritForm, shieldCritRow{ID: string(c), Label: c.Label(), Enabled: cp.Enabled, Mandatory: cp.Mandatory, Weight: fmtFloat(cp.Weight), Grace: cp.GraceDays})
	}
	var lv []string
	for _, l := range p.Levels {
		lv = append(lv, fmt.Sprintf("%d;%s", l.MinDays, l.Name))
	}
	v.Levels = strings.Join(lv, "\n")
	var bonus []string
	for _, b := range p.XP.StreakBonus {
		bonus = append(bonus, fmt.Sprintf("%d;%d", b.Days, b.Points))
	}
	v.Bonus = strings.Join(bonus, "\n")
	v.Holidays = strings.Join(p.Holidays, "\n")
	ach, _ := json.MarshalIndent(p.Achievements, "", "  ")
	v.Achievements = string(ach)
	v.Credits = map[string]string{}
	for _, s := range []shield.Status{shield.Parcial, shield.DentroPrazo, shield.AguardandoTI, shield.AguardandoUsuario} {
		v.Credits[string(s)] = fmtFloat(p.Credits[s])
	}
	hist, err := db.ShieldPolicyHistory(ctx, 20)
	if err != nil {
		return err
	}
	for _, h := range hist {
		v.History = append(v.History, shieldVersionRow{Version: h.Version, By: h.CreatedBy, At: a.stamp(h.CreatedAt), Note: h.Note})
	}
	return nil
}

func (a *api) shieldAuditTab(ctx context.Context, v *shieldPanelView) error {
	ev, err := a.store.AuditEvents(ctx, store.AuditFilter{Action: "shield.", Limit: 200})
	if err != nil {
		return err
	}
	for _, e := range ev {
		v.Audit = append(v.Audit, shieldAuditRow{At: a.stamp(e.At), User: e.Username, Action: e.Action, Target: e.Target, Detail: e.Detail})
	}
	return nil
}

// --- Ações (admin) ---

// shieldBack volta para a aba com a mensagem.
func shieldBack(w http.ResponseWriter, r *http.Request, tab, query string) {
	http.Redirect(w, r, "/painel/shield?aba="+tab+"&"+query, http.StatusSeeOther)
}

func shieldFail(w http.ResponseWriter, r *http.Request, tab string, err error) {
	shieldBack(w, r, tab, "erro=1&motivo="+url.QueryEscape(err.Error()))
}

var errShieldForm = errors.New("formulário inválido")

// parseNum lê um número com vírgula ou ponto.
func parseNum(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("número inválido %q", shortError(s, 20))
	}
	return f, nil
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("número inteiro inválido %q", shortError(s, 20))
	}
	return n, nil
}

// shieldPolicyFrom monta a política do formulário, por cima da atual (o que o formulário não
// traz fica como está).
func shieldPolicyFrom(r *http.Request, base shield.Policy) (shield.Policy, error) {
	p := base
	var errs []error
	num := func(field string, dst *float64) {
		if f, err := parseNum(r.PostFormValue(field)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, err))
		} else {
			*dst = f
		}
	}
	integer := func(field string, dst *int) {
		if n, err := parseInt(r.PostFormValue(field)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, err))
		} else {
			*dst = n
		}
	}
	p.Enabled = r.PostFormValue("ligado") == "1"
	p.PilotRings = nil
	for _, s := range r.PostForm["piloto"] {
		n, err := parseInt(s)
		if err != nil || n < 0 || n > 9 {
			errs = append(errs, errors.New("grupo piloto inválido"))
			continue
		}
		if !slices.Contains(p.PilotRings, n) {
			p.PilotRings = append(p.PilotRings, n)
		}
	}
	slices.Sort(p.PilotRings)
	p.Criteria = map[shield.Criterion]shield.CriterionPolicy{}
	for _, c := range shield.Criteria {
		cp := shield.CriterionPolicy{Enabled: r.PostFormValue(string(c)+"_on") == "1", Mandatory: r.PostFormValue(string(c)+"_obrig") == "1"}
		num(string(c)+"_peso", &cp.Weight)
		integer(string(c)+"_prazo", &cp.GraceDays)
		p.Criteria[c] = cp
	}
	if r.PostForm.Has("av_windows") || r.PostForm.Has("av_linux") || r.PostForm.Has("av_macos") {
		parseServices := func(field string) []string {
			var out []string
			for _, line := range strings.Split(r.PostFormValue(field), "\n") {
				if n := strings.TrimSpace(line); n != "" && !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
			return out
		}
		p.AntivirusServices = shield.AntivirusServices{Windows: parseServices("av_windows"), Linux: parseServices("av_linux"), MacOS: parseServices("av_macos")}
	}
	p.Credits = map[shield.Status]float64{shield.Conforme: 1, shield.NaoConforme: 0, shield.Desconhecido: 0}
	for _, s := range []shield.Status{shield.Parcial, shield.DentroPrazo, shield.AguardandoTI, shield.AguardandoUsuario} {
		var f float64
		num("cred_"+string(s), &f)
		p.Credits[s] = f
	}
	integer("inv_dias", &p.InventoryDays)
	integer("inv_limite", &p.InventoryHardDays)
	integer("criticas_prazo", &p.CriticalGraceDays)
	integer("quase", &p.NearMin)
	integer("atencao", &p.AttentionMin)
	p.Workdays = nil
	for _, s := range r.PostForm["dias"] {
		if n, err := parseInt(s); err == nil && n >= 0 && n <= 6 && !slices.Contains(p.Workdays, n) {
			p.Workdays = append(p.Workdays, n)
		}
	}
	slices.Sort(p.Workdays)
	p.Holidays = nil
	for _, l := range strings.Split(r.PostFormValue("feriados"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			p.Holidays = append(p.Holidays, l)
		}
	}
	p.Timezone = strings.TrimSpace(r.PostFormValue("fuso"))
	p.Levels = nil
	for i, l := range strings.Split(r.PostFormValue("niveis"), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		d, name, ok := strings.Cut(l, ";")
		n, err := parseInt(d)
		if !ok || err != nil {
			errs = append(errs, fmt.Errorf("nível na linha %d: use dias;nome", i+1))
			continue
		}
		p.Levels = append(p.Levels, shield.Level{Name: strings.TrimSpace(name), MinDays: n})
	}
	if s := strings.TrimSpace(r.PostFormValue("conquistas")); s != "" {
		var list []shield.Achievement
		dec := json.NewDecoder(strings.NewReader(s))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&list); err != nil {
			errs = append(errs, fmt.Errorf("conquistas: JSON inválido (%s)", shortError(err.Error(), 80)))
		} else {
			p.Achievements = list
		}
	}
	integer("xp_dia", &p.XP.ProtectedDay)
	integer("xp_reinicio", &p.XP.Reboot)
	integer("xp_atualizacao", &p.XP.Update)
	integer("xp_teto", &p.XP.MaxPerDay)
	p.XP.StreakBonus = nil
	for i, l := range strings.Split(r.PostFormValue("bonus"), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		d, pts, ok := strings.Cut(l, ";")
		dn, err1 := parseInt(d)
		pn, err2 := parseInt(pts)
		if !ok || err1 != nil || err2 != nil {
			errs = append(errs, fmt.Errorf("bônus na linha %d: use dias;pontos", i+1))
			continue
		}
		p.XP.StreakBonus = append(p.XP.StreakBonus, shield.StreakBonus{Days: dn, Points: pn})
	}
	p.Brand = shield.Brand{Name: strings.TrimSpace(r.PostFormValue("marca")), Mission: strings.TrimSpace(r.PostFormValue("missao")),
		Slogan: strings.TrimSpace(r.PostFormValue("slogan"))}
	p.Ranking.Collective = r.PostFormValue("rank_coletivo") == "1"
	p.Ranking.Individual = r.PostFormValue("rank_individual") == "1"
	p.Ranking.Buildings = r.PostFormValue("rank_predios") == "1"
	p.Ranking.Floors = r.PostFormValue("rank_andares") == "1"
	p.Ranking.Rooms = r.PostFormValue("rank_salas") == "1"
	integer("rank_min", &p.Ranking.MinGroup)
	integer("rank_min_sala", &p.Ranking.MinRoom)
	p.Notify.Enabled = r.PostFormValue("avisos") == "1"
	integer("avisos_horas", &p.Notify.MinHours)
	integer("exp_inicio", &p.Notify.WorkStart)
	integer("exp_fim", &p.Notify.WorkEnd)
	if err := errors.Join(errs...); err != nil {
		return p, err
	}
	return p, p.Validate()
}

// policyDiff: as chaves de primeiro nível que mudaram (para a auditoria).
func policyDiff(a, b shield.Policy) string {
	var x, y map[string]json.RawMessage
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	_ = json.Unmarshal(ja, &x)
	_ = json.Unmarshal(jb, &y)
	var changed []string
	for k, v := range y {
		if !bytes.Equal(x[k], v) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return "sem mudança"
	}
	return "mudou: " + strings.Join(changed, ", ")
}

// shieldPolicyAction: POST /painel/shield/politica.
func (a *api) shieldPolicyAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "politica", errShieldOff)
		return
	}
	if err := r.ParseForm(); err != nil {
		shieldFail(w, r, "politica", errShieldForm)
		return
	}
	cur, _, err := a.shieldPolicy(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a política do Shield", err)
		return
	}
	pol, err := shieldPolicyFrom(r, cur)
	if err != nil {
		shieldFail(w, r, "politica", err)
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if len([]rune(note)) > 200 {
		shieldFail(w, r, "politica", errors.New("nota de até 200 caracteres"))
		return
	}
	ver, err := db.SaveShieldPolicy(r.Context(), pol, note, s.Username)
	if err != nil {
		shieldFail(w, r, "politica", err)
		return
	}
	a.shieldPolicyStale()
	detail := fmt.Sprintf("versão %d; %s; ligado=%v; piloto=%v", ver, policyDiff(cur, pol), pol.Enabled, pol.PilotRings)
	if note != "" {
		detail += "; nota: " + note
	}
	a.audit(r, s, "shield.politica", fmt.Sprintf("versão %d", ver), detail)
	shieldBack(w, r, "politica", "politica="+strconv.Itoa(ver))
}

// parseDay lê AAAA-MM-DD no fuso loc.
func parseDay(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(s), loc)
	if err != nil {
		return t, fmt.Errorf("data inválida %q (use AAAA-MM-DD)", shortError(s, 20))
	}
	return t, nil
}

// shieldMissionAction: POST /painel/shield/missoes (uma missão nova).
func (a *api) shieldMissionAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "missoes", errShieldOff)
		return
	}
	pol, _, err := a.shieldPolicy(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a política do Shield", err)
		return
	}
	loc := pol.Location(a.panelLocation())
	m := store.ShieldMission{Title: r.PostFormValue("titulo"), Description: strings.TrimSpace(r.PostFormValue("descricao")),
		Scope: r.PostFormValue("tipo"), State: "rascunho", CreatedBy: s.Username}
	if r.PostFormValue("publicar") == "1" {
		m.State = "publicada"
	}
	var errs []error
	start, err := parseDay(r.PostFormValue("inicio"), loc)
	errs = append(errs, err)
	end, err := parseDay(r.PostFormValue("fim"), loc)
	errs = append(errs, err)
	m.StartsAt, m.EndsAt = start.UTC(), end.AddDate(0, 0, 1).Add(-time.Second).UTC()
	if m.XP, err = parseInt(r.PostFormValue("xp")); err != nil {
		errs = append(errs, err)
	}
	if g := r.PostFormValue("grupo"); g != "" {
		n, err := parseInt(g)
		if err != nil || n < 0 || n > 9 {
			errs = append(errs, errors.New("grupo inválido"))
		}
		m.Ring = &n
	}
	switch m.Scope {
	case "individual":
		m.Goal.Days, err = parseInt(r.PostFormValue("dias"))
		errs = append(errs, err)
	case "coletiva":
		m.Goal.Percent, err = parseInt(r.PostFormValue("percentual"))
		errs = append(errs, err)
		m.Goal.Criterion = shield.Criterion(r.PostFormValue("criterio"))
	}
	if err := errors.Join(errs...); err != nil {
		shieldFail(w, r, "missoes", err)
		return
	}
	id, err := db.SaveShieldMission(r.Context(), m)
	if errors.Is(err, store.ErrShieldMission) {
		shieldFail(w, r, "missoes", err)
		return
	}
	if err != nil {
		a.internalError(w, r, "gravar a missão", err)
		return
	}
	a.audit(r, s, "shield.missao", fmt.Sprintf("missão %d", id), fmt.Sprintf("%s (%s, %s): %s a %s, %d XP", m.Title, m.Scope, m.State,
		start.Format(time.DateOnly), end.Format(time.DateOnly), m.XP))
	shieldBack(w, r, "missoes", "missao=1")
}

// shieldMissionStateAction: POST /painel/shield/missoes/{id}/estado (publicar ou cancelar).
func (a *api) shieldMissionStateAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "missoes", errShieldOff)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	state := r.PostFormValue("estado")
	if err != nil || (state != "publicada" && state != "cancelada") {
		shieldFail(w, r, "missoes", errShieldForm)
		return
	}
	done, err := db.SetShieldMissionState(r.Context(), id, state)
	if err != nil {
		a.internalError(w, r, "mudar a missão", err)
		return
	}
	if !done {
		shieldFail(w, r, "missoes", errors.New("a missão não está numa situação que permite isso"))
		return
	}
	a.audit(r, s, "shield.missao_estado", fmt.Sprintf("missão %d", id), state)
	shieldBack(w, r, "missoes", "estado=1")
}

// shieldExceptionAction: POST /painel/shield/excecoes.
func (a *api) shieldExceptionAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "excecoes", errShieldOff)
		return
	}
	prefix := strings.TrimSpace(r.PostFormValue("maquina"))
	days, err := parseInt(r.PostFormValue("dias"))
	if err != nil || prefix == "" || len(prefix) > 64 || days < 1 || days > 365 {
		shieldFail(w, r, "excecoes", errors.New("informe o ID da máquina (ou o começo dele) e a validade de 1 a 365 dias"))
		return
	}
	id, found, err := db.ResolveMachine(r.Context(), prefix)
	if err != nil {
		shieldFail(w, r, "excecoes", errors.New("o começo do ID serve para mais de uma máquina: use o ID completo"))
		return
	}
	if !found {
		shieldFail(w, r, "excecoes", errors.New("máquina não encontrada"))
		return
	}
	now := a.now()
	e := store.ShieldException{MachineID: id, Criterion: shield.Criterion(r.PostFormValue("criterio")), Reason: r.PostFormValue("motivo"),
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedBy: s.Username}
	eid, err := db.AddShieldException(r.Context(), e, now)
	if errors.Is(err, store.ErrShieldException) || errors.Is(err, store.ErrShieldNotFound) {
		shieldFail(w, r, "excecoes", err)
		return
	}
	if err != nil {
		a.internalError(w, r, "gravar a exceção", err)
		return
	}
	a.audit(r, s, "shield.excecao", id, fmt.Sprintf("exceção %d: %s por %d dia(s): %s", eid, e.Criterion, days, strings.TrimSpace(e.Reason)))
	shieldBack(w, r, "excecoes", "excecao=1")
}

// shieldRevokeAction: POST /painel/shield/excecoes/{id}/revogar.
func (a *api) shieldRevokeAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "excecoes", errShieldOff)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		shieldFail(w, r, "excecoes", errShieldForm)
		return
	}
	e, done, err := db.RevokeShieldException(r.Context(), id, s.Username)
	if err != nil {
		a.internalError(w, r, "revogar a exceção", err)
		return
	}
	if !done {
		shieldFail(w, r, "excecoes", errors.New("a exceção já estava revogada"))
		return
	}
	a.audit(r, s, "shield.excecao_revogar", e.MachineID, fmt.Sprintf("exceção %d (%s)", e.ID, e.Criterion))
	shieldBack(w, r, "excecoes", "revogada=1")
}

var refRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// shieldAdjustAction: POST /painel/shield/pessoas/{id}/ajuste — a compensação do administrador
// (com motivo, no livro do XP e na auditoria). Não existe outra forma de dar XP pelo painel.
func (a *api) shieldAdjustAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	db, ok := a.shieldDB()
	if !ok {
		shieldFail(w, r, "pessoas", errShieldOff)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ref := r.PostFormValue("ref")
	if err != nil || !refRe.MatchString(ref) {
		shieldFail(w, r, "pessoas", errShieldForm)
		return
	}
	points, err := parseInt(r.PostFormValue("pontos"))
	if err != nil {
		shieldFail(w, r, "pessoas", err)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("motivo"))
	pls, err := db.ShieldPlayersByID(r.Context(), []int64{id})
	if err != nil {
		a.internalError(w, r, "ler a pessoa", err)
		return
	}
	pl, found := pls[id]
	if !found {
		shieldFail(w, r, "pessoas", errors.New("pessoa não encontrada"))
		return
	}
	done, err := db.AdjustShieldXP(r.Context(), id, points, reason, s.Username, "adm:"+ref, a.now())
	if errors.Is(err, store.ErrShieldAdjust) {
		shieldFail(w, r, "pessoas", err)
		return
	}
	if err != nil {
		a.internalError(w, r, "ajustar o XP", err)
		return
	}
	if !done {
		shieldBack(w, r, "pessoas", "repetido=1")
		return
	}
	a.audit(r, s, "shield.ajuste_xp", pl.Account, fmt.Sprintf("%+d XP: %s", points, reason))
	shieldBack(w, r, "pessoas", "ajuste=1")
}

// shieldMachinesCSV: GET /painel/shield/equipamentos.csv.
func (a *api) shieldMachinesCSV(w http.ResponseWriter, r *http.Request) {
	db, ok := a.shieldDB()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "shield_off", errShieldOff.Error())
		return
	}
	names, err := a.ringNames(r.Context())
	if err != nil {
		a.internalError(w, r, "ler os grupos", err)
		return
	}
	rows, err := a.shieldMachineRows(r.Context(), db, names)
	if err != nil {
		a.internalError(w, r, "montar o CSV", err)
		return
	}
	data := make([][]string, 0, len(rows))
	for _, m := range rows {
		data = append(data, []string{m.ID, m.Name, m.Ring, strconv.Itoa(m.Display), strconv.FormatBool(m.Sealed), m.OverallLabel, m.Blame, m.Blockers, m.Evaluated, m.Changed})
	}
	name := "patchd-shield-equipamentos-" + a.now().Format("20060102-1504") + ".csv"
	a.sendCSV(w, r, name, len(data), func(b *bytes.Buffer) error {
		return export.Table(b, []string{"maquina", "nome", "grupo", "protecao_pct", "selo", "faixa", "responsavel", "falta_para_o_selo", "avaliada_em", "mudou_em"}, data)
	})
}

// shieldPlayersCSV: GET /painel/shield/pessoas.csv.
func (a *api) shieldPlayersCSV(w http.ResponseWriter, r *http.Request) {
	db, ok := a.shieldDB()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "shield_off", errShieldOff.Error())
		return
	}
	pol, _, err := a.shieldPolicy(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a política do Shield", err)
		return
	}
	list, _, err := db.ShieldPlayerList(r.Context(), "", 20000, 0)
	if err != nil {
		a.internalError(w, r, "montar o CSV", err)
		return
	}
	data := make([][]string, 0, len(list))
	for _, p := range list {
		level := ""
		if cur, _ := pol.LevelFor(p.Streak.Protected); cur >= 0 {
			level = pol.Levels[cur].Name
		}
		data = append(data, []string{p.Account, p.DisplayName, strconv.FormatInt(p.XP, 10), strconv.Itoa(p.Streak.Current), strconv.Itoa(p.Streak.Best),
			strconv.Itoa(p.Streak.Protected), level, export.When(p.SeenAt)})
	}
	name := "patchd-shield-pessoas-" + a.now().Format("20060102-1504") + ".csv"
	a.sendCSV(w, r, name, len(data), func(b *bytes.Buffer) error {
		return export.Table(b, []string{"conta", "nome", "xp", "sequencia", "melhor_sequencia", "dias_protegidos", "nivel", "visto_em_utc"}, data)
	})
}

var _ = template.Must(pages.New("shield").Parse(`{{template "topo" .}}
<div class="shield-admin"><header class="ux-hero"><div><span class="ux-eyebrow">Segurança e conformidade</span><h1>{{if .On}}{{.Pol.Brand.Name}}{{else}}Shield{{end}}</h1><p>{{if .On}}{{.Pol.Brand.Mission}} · {{end}}Acompanhe a proteção dos equipamentos e a colaboração dos participantes.</p></div><div class="ux-hero-side">{{if .On}}{{if .Pol.Enabled}}<span class="ux-status ux-status-on">Shield ativo</span>{{else}}<span class="ux-status ux-status-off">Shield desativado</span>{{end}}{{if .LastRun}}<span>Última avaliação: {{.LastRun}}</span>{{end}}{{end}}</div></header>
{{if not .On}}<p class="aviso">O Shield precisa do PostgreSQL (este servidor está com o banco em memória).</p>{{else}}
<nav class="abas" aria-label="abas do Shield">{{range .Tabs}}<a href="/painel/shield?aba={{.ID}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<p class="ux-meta">{{if .Pol.Enabled}}<b>Ligado</b> para {{.Scope}}{{else}}<b>Desligado</b>: nenhuma máquina é avaliada e a Central não mostra a Minha Proteção{{if .Admin}} (ligue em <a href="/painel/shield?aba=politica">Política</a>){{end}}{{end}}.
Política versão {{if .Version}}{{.Version}}{{if .PolBy}} ({{.PolBy}}, {{.PolAt}}){{end}}{{else}}padrão{{end}}.{{if .LastRun}} Última avaliação: {{.LastRun}}.{{end}}</p>
{{with .LastErr}}<p class="aviso erro">A última avaliação falhou: {{.}}</p>{{end}}
{{if eq .Tab "geral"}}{{template "shield-geral" .}}{{else if eq .Tab "equipamentos"}}{{template "shield-equip" .}}{{else if eq .Tab "pessoas"}}{{template "shield-pessoas" .}}{{else if eq .Tab "missoes"}}{{template "shield-missoes" .}}{{else if eq .Tab "ranking"}}{{template "shield-ranking" .}}{{else if eq .Tab "excecoes"}}{{template "shield-excecoes" .}}{{else if eq .Tab "politica"}}{{template "shield-politica" .}}{{else}}{{template "shield-auditoria" .}}{{end}}
{{end}}</div>
{{template "fim"}}`))

var _ = template.Must(pages.New("shield-geral").Parse(`<ul class="kpis shield-primary">
<li><div class="kpi"><span class="kpi-rot">Máquinas no Shield</span><span class="kpi-val">{{.Total}}</span><span class="kpi-sub">com pontuação</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Selo 100%</span><span class="kpi-val {{cor "v-verde" .Sealed}}">{{.SealedPct}}%</span><span class="kpi-sub">{{.Sealed}} máquina(s); as sem dado contam no total</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Média</span><span class="kpi-val">{{if .Average}}{{.Average}}{{else}}—{{end}}</span><span class="kpi-sub">da pontuação (sem dado vale 0)</span></div></li></ul>
<div class="card"><div class="card-cab"><h2>Distribuição por estado de proteção</h2><a href="/painel/shield/equipamentos.csv">exportar CSV</a></div>
<ul class="kpis">{{range .Counts}}<li><a class="kpi" href="{{.Href}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{cor .Class .N}}">{{.N}}</span></a></li>{{end}}</ul></div>
{{if .Blames}}<div class="card"><h2>Responsabilidade pelas pendências</h2><table><thead><tr><th>Responsável principal</th><th>Máquinas</th></tr></thead><tbody>{{range .Blames}}<tr><td>{{.Label}}</td><td>{{.N}}</td></tr>{{end}}</tbody></table>
<p class="muted">O responsável principal é o do maior peso perdido. A sequência da pessoa só quebra com pendência vencida que é dela.</p></div>{{end}}
{{if .Crit}}<div class="card"><h2>Conformidade por critério</h2><table><thead><tr><th>Critério</th><th>Conforme</th><th>Fora</th><th>Desconhecido</th><th>% conforme</th></tr></thead><tbody>
{{range .Crit}}<tr><td>{{.Label}}</td><td>{{.Conforme}}</td><td>{{.Fora}}</td><td>{{.Unknown}}</td><td class="shield-percent"><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td></tr>{{end}}</tbody></table></div>{{end}}
<div class="card"><h2>Evolução (fim de cada dia, últimos 30 dias)</h2>{{if .Trend}}<table><thead><tr><th>Dia</th><th>Máquinas</th><th>No selo</th><th>% no selo</th><th>Média</th></tr></thead><tbody>
{{range .Trend}}<tr><td>{{.Day}}</td><td>{{.Machines}}</td><td>{{.Sealed}}</td><td><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td><td>{{.Avg}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Ainda sem histórico: o primeiro retrato do dia aparece depois da primeira avaliação com o Shield ligado.</p>{{end}}</div>`))

var _ = template.Must(pages.New("shield-equip").Parse(`<div class="card"><div class="card-cab"><h2>Equipamentos ({{.Hit}})</h2><a href="/painel/shield/equipamentos.csv">exportar CSV</a></div>
<form class="filtros" method="get" action="/painel/shield"><input type="hidden" name="aba" value="equipamentos">{{if .Filter}}<input type="hidden" name="faixa" value="{{.Filter}}">{{end}}<label for="q-shield" class="sr">Buscar</label><input id="q-shield" type="search" name="q" value="{{.Q}}" placeholder="nome, ID ou grupo" maxlength="100"><button type="submit">Buscar</button></form>
<p class="shield-filters"><span class="muted">Filtrar:</span> <a href="/painel/shield?aba=equipamentos"{{if not .Filter}} aria-current="page"{{end}}>todas</a>{{range .Counts}} <a href="{{.Href}}"{{if .Active}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</p>
{{if .Machines}}<table><thead><tr><th>Máquina</th><th>Grupo</th><th>Proteção</th><th>Faixa</th><th>Responsável</th><th>Falta para o selo</th><th>Mudou em</th></tr></thead><tbody>
{{range .Machines}}<tr><td><a href="/painel/maquinas/{{.ID}}">{{.Name}}</a></td><td>{{.Ring}}</td><td>{{if .Sealed}}✓ 100%{{else}}{{.Display}}%{{end}}</td><td>{{.OverallLabel}}</td><td>{{if .Blame}}{{.Blame}}{{else}}—{{end}}</td><td>{{if .Blockers}}{{.Blockers}}{{else}}—{{end}}</td><td>{{.Changed}}</td></tr>{{end}}</tbody></table>
{{if or .PrevHref .NextHref}}<p class="paginas">{{if .PrevHref}}<a href="{{.PrevHref}}">‹ anterior</a>{{end}} página {{.Page}} de {{.Pages}} {{if .NextHref}}<a href="{{.NextHref}}">próxima ›</a>{{end}}</p>{{end}}
{{else}}<p class="vazio">Nenhuma máquina{{if or .Filter .Q}} com esse filtro{{else}} com pontuação ainda (ligue o Shield na Política e aguarde a primeira avaliação){{end}}.</p>{{end}}</div>`))

var _ = template.Must(pages.New("shield-pessoas").Parse(`<div class="card"><div class="card-cab"><h2>Pessoas ({{.Hit}})</h2><a href="/painel/shield/pessoas.csv">exportar CSV</a></div>
<form class="filtros" method="get" action="/painel/shield"><input type="hidden" name="aba" value="pessoas"><label for="q-pessoa" class="sr">Buscar</label><input id="q-pessoa" type="search" name="q" value="{{.Q}}" placeholder="conta ou nome" maxlength="100"><button type="submit">Buscar</button></form>
{{if .Players}}<table><thead><tr><th>Pessoa</th><th>Nível</th><th>XP</th><th>Sequência</th><th>Dias protegidos</th><th>Vista em</th>{{if .Admin}}<th>Ajuste de XP</th>{{end}}</tr></thead><tbody>
{{range .Players}}<tr><td>{{if .Name}}{{.Name}}<br><span class="muted">({{.Account}})</span>{{else}}{{.Account}}{{end}}</td><td>{{if .Level}}{{.Level}}{{else}}—{{end}}</td><td>{{.XP}}</td><td>{{.Streak}} <span class="muted">(melhor {{.Best}})</span></td><td>{{.Protected}}</td><td>{{.Seen}}</td>
{{if $.Admin}}<td><form class="linha" method="post" action="/painel/shield/pessoas/{{.ID}}/ajuste"><input type="hidden" name="ref" value="{{$.Ref}}"><label class="sr" for="p{{.ID}}">Pontos</label><input id="p{{.ID}}" name="pontos" type="number" min="-10000" max="10000" required placeholder="±XP"><label class="sr" for="m{{.ID}}">Motivo</label><input id="m{{.ID}}" name="motivo" maxlength="200" required placeholder="motivo (obrigatório)"><button class="sec" type="submit">Ajustar</button></form></td>{{end}}</tr>{{end}}</tbody></table>
{{if or .PrevHref .NextHref}}<p class="paginas">{{if .PrevHref}}<a href="{{.PrevHref}}">‹ anterior</a>{{end}} página {{.Page}} de {{.Pages}} {{if .NextHref}}<a href="{{.NextHref}}">próxima ›</a>{{end}}</p>{{end}}
<p class="muted">O XP vem só de eventos do servidor. O ajuste é a compensação do administrador (um erro, uma indisponibilidade): fica no livro do XP com o motivo e na auditoria.</p>
{{else}}<p class="vazio">Ninguém ainda. A pessoa aparece quando usa uma máquina do Shield (sessão no check-in) ou abre a Minha Proteção na Central.</p>{{end}}</div>`))

var _ = template.Must(pages.New("shield-missoes").Parse(`<div class="card"><h2>Missões</h2>{{if .Missions}}<table><thead><tr><th>Missão</th><th>Tipo</th><th>Meta</th><th>Escopo</th><th>Período</th><th>Situação</th><th>XP</th>{{if .Admin}}<th></th>{{end}}</tr></thead><tbody>
{{range .Missions}}<tr><td><b>{{.Title}}</b>{{if .Description}}<br><span class="muted">{{.Description}}</span>{{end}}</td><td>{{.Scope}}</td><td>{{.Goal}}{{if .Progress}}<br><meter min="0" max="100" value="{{.Percent}}">{{.Percent}}%</meter> <span class="muted">{{.Progress}}</span>{{end}}</td><td>{{.Scope2}}</td><td>{{.Period}}</td><td>{{.StatusLabel}}</td><td>{{.XP}}</td>
{{if $.Admin}}<td>{{if .CanPublish}}<form class="linha" method="post" action="/painel/shield/missoes/{{.ID}}/estado"><input type="hidden" name="estado" value="publicada"><button class="sec" type="submit">Publicar</button></form>{{end}}{{if .CanCancel}}<form class="linha" method="post" action="/painel/shield/missoes/{{.ID}}/estado"><input type="hidden" name="estado" value="cancelada"><button class="sec" type="submit">Cancelar</button></form>{{end}}</td>{{end}}</tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Nenhuma missão.{{if .Admin}} Crie a primeira abaixo.{{end}}</p>{{end}}
<p class="muted">A missão só mede: não cria job nem campanha. A individual paga quem tiver os dias protegidos no período; a coletiva, quando cumprida, paga quem usou máquinas do escopo no período.</p></div>
{{if .Admin}}<div class="card"><h2>Nova missão</h2><form class="formulario" method="post" action="/painel/shield/missoes">
<div class="campo"><label for="mt">Título</label><input id="mt" name="titulo" maxlength="80" required></div>
<div class="campo"><label for="md">Descrição</label><input id="md" name="descricao" maxlength="500"></div>
<div class="campo"><label for="mtp">Tipo</label><select id="mtp" name="tipo"><option value="individual">individual: dias protegidos no período</option><option value="coletiva">coletiva: % das máquinas do escopo</option></select></div>
<div class="campo"><label for="mdi">Dias protegidos (individual)</label><input id="mdi" name="dias" type="number" min="1" max="180" value="5"></div>
<div class="campo"><label for="mpc">Meta em % (coletiva)</label><input id="mpc" name="percentual" type="number" min="1" max="100" value="90"></div>
<div class="campo"><label for="mcr">Critério (coletiva; vazio: o selo 100%)</label><select id="mcr" name="criterio"><option value="">selo 100%</option>{{range .Criteria}}<option value="{{.}}">{{.Label}}</option>{{end}}</select></div>
<div class="campo"><label for="mgr">Escopo</label><select id="mgr" name="grupo"><option value="">todo o Shield</option>{{range .Rings}}<option value="{{.N}}">{{.Label}}</option>{{end}}</select></div>
<div class="campo"><label for="mi">Início</label><input id="mi" name="inicio" type="date" required></div>
<div class="campo"><label for="mf">Fim (até 180 dias)</label><input id="mf" name="fim" type="date" required></div>
<div class="campo"><label for="mx">XP</label><input id="mx" name="xp" type="number" min="0" max="10000" value="50"></div>
<div><label class="check"><input type="checkbox" name="publicar" value="1"> publicar já (senão, fica como rascunho)</label></div>
<button type="submit">Gravar a missão</button></form></div>{{end}}`))

var _ = template.Must(pages.New("shield-ranking").Parse(`<div class="card"><h2>Ranking coletivo por grupo de implantação</h2>
{{if not .Pol.Ranking.Collective}}<p class="vazio">O ranking coletivo está desligado na Política.</p>
{{else if .Groups}}<table><thead><tr><th>#</th><th>Grupo</th><th>Máquinas</th><th>No selo</th><th>% no selo</th><th>Média</th></tr></thead><tbody>
{{range .Groups}}<tr><td><span class="shield-rank{{if eq .Pos 1}} first{{else if eq .Pos 2}} second{{else if eq .Pos 3}} third{{end}}">{{.Pos}}º</span></td><td class="shield-rank-name">{{.Name}}</td><td>{{.Machines}}</td><td>{{.Sealed}}</td><td class="shield-percent"><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td><td>{{.Avg}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Nenhum grupo com o mínimo de máquinas.</p>{{end}}
{{if .Hidden}}<p class="muted">{{.Hidden}} grupo(s) com menos de {{.Pol.Ranking.MinGroup}} máquinas ficam de fora (privacidade).</p>{{end}}
<p class="muted">O denominador inclui as máquinas sem dado. O ranking nunca mostra máquina, vulnerabilidade ou programa.</p></div>
<div class="card"><h2>Ranking dos prédios</h2>{{if not .Pol.Ranking.Buildings}}<p class="vazio">Desligado na Política.</p>
{{else if .PlaceB}}<table><thead><tr><th>#</th><th>Prédio</th><th>Máquinas</th><th>No selo</th><th>% no selo</th><th>Média</th></tr></thead><tbody>
{{range .PlaceB}}<tr><td><span class="shield-rank{{if eq .Pos 1}} first{{else if eq .Pos 2}} second{{else if eq .Pos 3}} third{{end}}">{{.Pos}}º</span></td><td class="shield-rank-name">{{.Label}}</td><td>{{.Machines}}</td><td>{{.Sealed}}</td><td class="shield-percent"><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td><td>{{.Avg}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Nenhum prédio ainda: o prédio vem do patrimônio (Configuração › Patrimônio), ligado pelo tombo no nome da máquina, e precisa do mínimo de {{.Pol.Ranking.MinGroup}} máquinas no Shield.</p>{{end}}</div>
{{if .PlaceP}}<div class="card"><h2>Andares e salas, por prédio</h2>
<p class="muted">Os andares são identificados pelas faixas de IP. Nomes e agrupamentos são definidos em <a href="/painel/configuracao?aba=localizacoes">Configuração › Localizações</a>. A Central mostra somente o nome do andar.</p>
{{range .PlaceP}}<details class="lugar"><summary><b>{{localNome .Building}}</b> — {{len .Floors}} andar(es) e {{len .Rooms}} sala(s) no ranking</summary>
{{if .Floors}}<h3>Andares</h3><table><thead><tr><th>#</th><th>Andar</th><th>Máquinas</th><th>% no selo</th><th>Média</th></tr></thead><tbody>
{{range .Floors}}<tr><td><span class="shield-rank{{if eq .Pos 1}} first{{else if eq .Pos 2}} second{{else if eq .Pos 3}} third{{end}}">{{.Pos}}º</span></td><td class="shield-rank-name">{{.Label}}</td><td>{{.Machines}}</td><td class="shield-percent"><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td><td>{{.Avg}}</td></tr>{{end}}</tbody></table>{{end}}
{{if .Rooms}}<h3>Salas</h3><table><thead><tr><th>#</th><th>Sala</th><th>Máquinas</th><th>% no selo</th><th>Média</th><th>Entre todas as salas</th></tr></thead><tbody>
{{range .Rooms}}<tr><td><span class="shield-rank{{if eq .Pos 1}} first{{else if eq .Pos 2}} second{{else if eq .Pos 3}} third{{end}}">{{.Pos}}º</span></td><td class="shield-rank-name">{{.Label}}</td><td>{{.Machines}}</td><td class="shield-percent"><meter min="0" max="100" value="{{.Pct}}">{{.Pct}}%</meter> {{.Pct}}%</td><td>{{.Avg}}</td><td>{{.Overall}}º</td></tr>{{end}}</tbody></table>{{end}}
</details>{{end}}
<p class="muted">Salas com menos de {{.Pol.Ranking.MinRoom}} máquinas e andares com menos de {{.Pol.Ranking.MinGroup}} não aparecem (privacidade).</p></div>{{end}}
<div class="card"><h2>Ranking individual</h2>{{if not .Pol.Ranking.Individual}}<p class="vazio">Desligado (o padrão). Ligue na Política só se a sua instituição aprovar: ele mostra nome, XP, dias protegidos e sequência — nada da máquina.</p>
{{else if .Leaders}}<table><thead><tr><th>#</th><th>Pessoa</th><th>XP</th><th>Dias protegidos</th><th>Sequência</th></tr></thead><tbody>{{range .Leaders}}<tr><td><span class="shield-rank{{if eq .Pos 1}} first{{else if eq .Pos 2}} second{{else if eq .Pos 3}} third{{end}}">{{.Pos}}º</span></td><td class="shield-rank-name">{{.Name}}</td><td>{{.XP}}</td><td>{{.Protected}}</td><td>{{.Streak}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Ninguém com XP nos últimos 30 dias.</p>{{end}}</div>`))

var _ = template.Must(pages.New("shield-excecoes").Parse(`<div class="card"><div class="card-cab"><h2>Exceções {{if .AllExc}}(todas){{else}}vigentes{{end}}</h2>{{if .AllExc}}<a href="/painel/shield?aba=excecoes">só as vigentes</a>{{else}}<a href="/painel/shield?aba=excecoes&amp;todas=1">ver todas</a>{{end}}</div>
{{if .Exceptions}}<table><thead><tr><th>Máquina</th><th>Critério</th><th>Motivo</th><th>Vale até</th><th>Aprovada por</th><th>Situação</th>{{if .Admin}}<th></th>{{end}}</tr></thead><tbody>
{{range .Exceptions}}<tr><td><a href="/painel/maquinas/{{.MachineID}}">{{.Host}}</a></td><td>{{.Criterion}}</td><td>{{.Reason}}</td><td>{{.Expires}}</td><td>{{.By}} <span class="muted">({{.At}})</span></td><td>{{if .Revoked}}revogada {{.Revoked}}{{else if .Active}}vigente{{else}}vencida{{end}}</td>
{{if $.Admin}}<td>{{if .Active}}<form class="linha" method="post" action="/painel/shield/excecoes/{{.ID}}/revogar"><button class="sec" type="submit">Revogar</button></form>{{end}}</td>{{end}}</tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Nenhuma exceção{{if not .AllExc}} vigente{{end}}.</p>{{end}}
<p class="muted">A exceção tira o critério da conta da máquina até a validade (o risco aceito pela TI, com motivo). Num critério obrigatório, ela impede o selo 100%: exceção não é prova.</p></div>
{{if .Admin}}<div class="card"><h2>Nova exceção</h2><form class="formulario" method="post" action="/painel/shield/excecoes">
<div class="campo"><label for="em">Máquina (ID ou o começo dele)</label><input id="em" name="maquina" maxlength="64" required></div>
<div class="campo"><label for="ec">Critério</label><select id="ec" name="criterio">{{range .Criteria}}<option value="{{.}}">{{.Label}}</option>{{end}}</select></div>
<div class="campo"><label for="emo">Motivo</label><input id="emo" name="motivo" maxlength="300" required></div>
<div class="campo"><label for="ed">Validade (dias, até 365)</label><input id="ed" name="dias" type="number" min="1" max="365" value="30" required></div>
<button type="submit">Registrar a exceção</button></form></div>{{end}}`))

var _ = template.Must(pages.New("shield-politica").Parse(`{{if not .Admin}}<p class="aviso">Só o administrador muda a política.</p>{{end}}
<form class="formulario" method="post" action="/painel/shield/politica">
<div class="card"><h2>Alcance</h2>
<div><label class="check"><input type="checkbox" name="ligado" value="1"{{if .Pol.Enabled}} checked{{end}}> Shield ligado</label></div>
<fieldset class="campo"><legend>Grupos piloto (nenhum marcado: todos os grupos)</legend><div class="opcoes-linha">{{range .Rings}}<label class="check"><input type="checkbox" name="piloto" value="{{.N}}"{{if .Checked}} checked{{end}}> {{.Label}}</label>{{end}}</div></fieldset></div>
<div class="card campos-shield"><h2>Critérios</h2><table><thead><tr><th>Critério</th><th>Ligado</th><th>Peso</th><th>Obrigatório p/ selo</th><th>Prazo (dias)</th></tr></thead><tbody>
{{range .CritForm}}<tr><td>{{.Label}}</td><td><input type="checkbox" name="{{.ID}}_on" value="1" aria-label="{{.Label}} ligado"{{if .Enabled}} checked{{end}}></td><td><input name="{{.ID}}_peso" value="{{.Weight}}" size="5" aria-label="peso de {{.Label}}"></td><td><input type="checkbox" name="{{.ID}}_obrig" value="1" aria-label="{{.Label}} obrigatório"{{if .Mandatory}} checked{{end}}></td><td><input name="{{.ID}}_prazo" type="number" min="0" max="90" value="{{.Grace}}" aria-label="prazo de {{.Label}}"></td></tr>{{end}}</tbody></table>
<h3 class="sub">Serviços obrigatórios do antivírus</h3>
<p class="muted">Configure os nomes internos dos serviços, um por linha. Todos devem estar em execução. Compatível com qualquer fabricante. Sem configuração ou coleta recente, o critério fica desconhecido. Serviço em execução não comprova atualização das assinaturas nem proteção efetiva.</p>
<div class="campo"><label for="avw">Windows — nomes de serviço (ServiceName)</label><textarea id="avw" name="av_windows" rows="4">{{range .Pol.AntivirusServices.Windows}}{{.}}{{"\n"}}{{end}}</textarea></div>
<div class="campo"><label for="avl">Linux — unidades systemd (.service)</label><textarea id="avl" name="av_linux" rows="2">{{range .Pol.AntivirusServices.Linux}}{{.}}{{"\n"}}{{end}}</textarea></div>
<div class="campo"><label for="avm">macOS — labels do launchd</label><textarea id="avm" name="av_macos" rows="2">{{range .Pol.AntivirusServices.MacOS}}{{.}}{{"\n"}}{{end}}</textarea></div>
<p class="muted">O critério vem desligado. Atualize os agentes e configure os nomes antes de ligá-lo na tabela acima.</p>
<h3 class="sub">Quanto cada situação vale (fração do peso; conforme vale 1, não conforme e desconhecido valem 0 — fixo)</h3>
<div class="campo"><label for="c1">Parcial</label><input id="c1" name="cred_parcial" value="{{index .Credits "parcial"}}"></div>
<div class="campo"><label for="c2">Dentro do prazo</label><input id="c2" name="cred_dentro_do_prazo" value="{{index .Credits "dentro_do_prazo"}}"></div>
<div class="campo"><label for="c3">Aguardando a TI</label><input id="c3" name="cred_aguardando_ti" value="{{index .Credits "aguardando_ti"}}"></div>
<div class="campo"><label for="c4">Aguardando o usuário</label><input id="c4" name="cred_aguardando_usuario" value="{{index .Credits "aguardando_usuario"}}"></div>
<div class="campo"><label for="i1">Inventário parcial depois de (dias)</label><input id="i1" name="inv_dias" type="number" value="{{.Pol.InventoryDays}}"></div>
<div class="campo"><label for="i2">Inventário não conforme depois de (dias)</label><input id="i2" name="inv_limite" type="number" value="{{.Pol.InventoryHardDays}}"></div>
<div class="campo"><label for="i3">Prazo das correções críticas ou exploradas (dias)</label><input id="i3" name="criticas_prazo" type="number" value="{{.Pol.CriticalGraceDays}}"></div>
<div class="campo"><label for="f1">Faixa "quase lá" a partir de (%)</label><input id="f1" name="quase" type="number" value="{{.Pol.NearMin}}"></div>
<div class="campo"><label for="f2">Faixa "atenção" a partir de (%)</label><input id="f2" name="atencao" type="number" value="{{.Pol.AttentionMin}}"></div></div>
<div class="card"><h2>Sequência, níveis e XP</h2>
<fieldset class="campo"><legend>Dias que contam</legend><div class="opcoes-linha">{{range .Days}}<label class="check"><input type="checkbox" name="dias" value="{{.N}}"{{if .Checked}} checked{{end}}> {{.Label}}</label>{{end}}</div></fieldset>
<div class="campo"><label for="fe">Feriados (AAAA-MM-DD, um por linha)</label><textarea id="fe" name="feriados" rows="4">{{.Holidays}}</textarea></div>
<div class="campo"><label for="fu">Fuso (vazio: o do painel)</label><input id="fu" name="fuso" value="{{.Pol.Timezone}}" maxlength="60"></div>
<div class="campo"><label for="nv">Níveis (dias protegidos;nome, um por linha, dias crescentes)</label><textarea id="nv" name="niveis" rows="7">{{.Levels}}</textarea></div>
<div class="campo"><label for="x1">XP por dia protegido</label><input id="x1" name="xp_dia" type="number" value="{{.Pol.XP.ProtectedDay}}"></div>
<div class="campo"><label for="x2">XP por reinício feito quando pedido</label><input id="x2" name="xp_reinicio" type="number" value="{{.Pol.XP.Reboot}}"></div>
<div class="campo"><label for="x3">XP por programa atualizado pela Central</label><input id="x3" name="xp_atualizacao" type="number" value="{{.Pol.XP.Update}}"></div>
<div class="campo"><label for="x4">Teto de XP por dia (sem missões e ajustes)</label><input id="x4" name="xp_teto" type="number" value="{{.Pol.XP.MaxPerDay}}"></div>
<div class="campo"><label for="bo">Bônus de sequência (dias;pontos, um por linha)</label><textarea id="bo" name="bonus" rows="4">{{.Bonus}}</textarea></div>
<div class="campo"><label for="cq">Conquistas (JSON: code, name, description, metric, min, xp)</label><textarea id="cq" name="conquistas" rows="12">{{.Achievements}}</textarea></div></div>
<div class="card"><h2>Textos, ranking e avisos</h2>
<div class="campo"><label for="b1">Nome do módulo</label><input id="b1" name="marca" value="{{.Pol.Brand.Name}}" maxlength="40"></div>
<div class="campo"><label for="b2">Nome da missão</label><input id="b2" name="missao" value="{{.Pol.Brand.Mission}}" maxlength="40"></div>
<div class="campo"><label for="b3">Slogan</label><input id="b3" name="slogan" value="{{.Pol.Brand.Slogan}}" maxlength="120"></div>
<div><label class="check"><input type="checkbox" name="rank_coletivo" value="1"{{if .Pol.Ranking.Collective}} checked{{end}}> Ranking coletivo por grupo</label></div>
<div><label class="check"><input type="checkbox" name="rank_individual" value="1"{{if .Pol.Ranking.Individual}} checked{{end}}> Ranking individual (nome, XP e sequência)</label></div>
<div><label class="check"><input type="checkbox" name="rank_predios" value="1"{{if .Pol.Ranking.Buildings}} checked{{end}}> Ranking dos prédios (do patrimônio)</label></div>
<div><label class="check"><input type="checkbox" name="rank_andares" value="1"{{if .Pol.Ranking.Floors}} checked{{end}}> Ranking dos andares (as faixas de IP, nos prédios com mais de uma)</label></div>
<div><label class="check"><input type="checkbox" name="rank_salas" value="1"{{if .Pol.Ranking.Rooms}} checked{{end}}> Ranking das salas (do patrimônio)</label></div>
<div class="campo"><label for="r2">Tamanho mínimo da sala no ranking</label><input id="r2" name="rank_min_sala" type="number" value="{{.Pol.Ranking.MinRoom}}"></div>
<div class="campo"><label for="r1">Tamanho mínimo do grupo, do prédio e do andar no ranking</label><input id="r1" name="rank_min" type="number" value="{{.Pol.Ranking.MinGroup}}"></div>
<div><label class="check"><input type="checkbox" name="avisos" value="1"{{if .Pol.Notify.Enabled}} checked{{end}}> Avisos do Shield perto do relógio</label></div>
<div class="campo"><label for="a1">No máximo um aviso a cada (horas)</label><input id="a1" name="avisos_horas" type="number" value="{{.Pol.Notify.MinHours}}"></div>
<div class="campo"><label for="a2">Expediente: das (hora)</label><input id="a2" name="exp_inicio" type="number" min="0" max="23" value="{{.Pol.Notify.WorkStart}}"></div>
<div class="campo"><label for="a3">até as (hora)</label><input id="a3" name="exp_fim" type="number" min="1" max="24" value="{{.Pol.Notify.WorkEnd}}"></div>
<div class="campo"><label for="no">Nota desta versão (opcional)</label><input id="no" name="nota" maxlength="200"></div>
{{if .Admin}}<button type="submit">Gravar como nova versão</button>{{end}}</div></form>
{{if .History}}<div class="card"><h2>Versões</h2><table><thead><tr><th>Versão</th><th>Quem</th><th>Quando</th><th>Nota</th></tr></thead><tbody>{{range .History}}<tr><td>{{.Version}}</td><td>{{.By}}</td><td>{{.At}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table></div>{{end}}`))

var _ = template.Must(pages.New("shield-auditoria").Parse(`<div class="card"><h2>Auditoria do Shield</h2>{{if .Audit}}<table><thead><tr><th>Quando</th><th>Quem</th><th>Ação</th><th>Alvo</th><th>Detalhe</th></tr></thead><tbody>
{{range .Audit}}<tr><td>{{.At}}</td><td>{{.User}}</td><td>{{.Action}}</td><td>{{.Target}}</td><td>{{.Detail}}</td></tr>{{end}}</tbody></table>
{{else}}<p class="vazio">Nenhuma ação no Shield ainda.</p>{{end}}<p class="muted">Política, missões, exceções, ajustes de XP e exportações. O XP decidido pelo servidor fica no livro do XP de cada pessoa.</p></div>`))
