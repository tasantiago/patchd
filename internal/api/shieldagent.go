package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// shieldAgent: POST /api/v1/agent/shield (docs/shield.md), com a credencial da máquina. A
// máquina é a da credencial (nunca um parâmetro); a pessoa é a que o agente leu do token do
// Windows. A resposta é só leitura: nada aqui dá XP. A visita conta como presença da pessoa
// na máquina hoje (o dia dela passa a contar com esta máquina).
func (a *api) shieldAgent(w http.ResponseWriter, r *http.Request, id string) {
	var u protocol.SelfServiceUser
	if !decodeJSON(w, r, &u) {
		return
	}
	if err := u.Check(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user", err.Error())
		return
	}
	db, ok := a.shieldDB()
	if !ok {
		writeJSON(w, http.StatusOK, protocol.ShieldView{Reason: "o Shield não está disponível neste servidor"})
		return
	}
	v, err := a.shieldView(r.Context(), db, id, u, a.now())
	if err != nil {
		a.internalError(w, r, "montar a Minha Proteção", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// shieldView monta a "Minha Proteção" da pessoa u na máquina id.
func (a *api) shieldView(ctx context.Context, db shieldStore, id string, u protocol.SelfServiceUser, now time.Time) (protocol.ShieldView, error) {
	pol, _, err := a.shieldPolicy(ctx)
	if err != nil {
		return protocol.ShieldView{}, err
	}
	v := protocol.ShieldView{Brand: pol.Brand.Name, Mission: pol.Brand.Mission, Slogan: pol.Brand.Slogan,
		Notify: protocol.ShieldNotify{Enabled: pol.Notify.Enabled, MinHours: pol.Notify.MinHours, WorkStart: pol.Notify.WorkStart, WorkEnd: pol.Notify.WorkEnd}}
	if !pol.Enabled {
		v.Reason = "o Shield está desligado"
		return v, nil
	}
	ring, found, err := db.MachineRing(ctx, id)
	if err != nil {
		return v, err
	}
	if !found || !pol.InScope(ring) {
		v.Reason = "este computador ainda não participa do Shield"
		return v, nil
	}
	v.Enabled = true
	loc := pol.Location(a.panelLocation())
	today := dateOf(now, loc)

	sc, have, err := db.ShieldScoreOf(ctx, id)
	if err != nil {
		return v, err
	}
	if have {
		v.Machine = shieldMachineView(sc.Result, sc.EvaluatedAt)
	} else {
		v.Machine = &protocol.ShieldMachineView{Pending: true, Overall: string(shield.NoData), OverallLabel: "em avaliação", Criteria: []protocol.ShieldCriterionView{}}
	}

	if !playable(store.NormalizeAccount(u.Account)) {
		return v, nil // conta de serviço: só a máquina
	}
	pl, err := db.ShieldPlayerFor(ctx, u.Account, u.SID, "")
	if err != nil {
		return v, err
	}
	if err := db.AddShieldPresence(ctx, []store.ShieldPresence{{PlayerID: pl.ID, MachineID: id, Day: today}}); err != nil {
		return v, err
	}
	pv := &protocol.ShieldPlayerView{Streak: pl.Streak.Current, Best: pl.Streak.Best, Protected: pl.Streak.Protected, XP: pl.XP}
	cur, next := pol.LevelFor(pl.Streak.Protected)
	if cur >= 0 {
		pv.Level = pol.Levels[cur].Name
	}
	if next != nil {
		pv.NextLevel, pv.NextLevelDays = next.Name, next.MinDays-pl.Streak.Protected
	}
	badges, err := db.ShieldBadgesOf(ctx, pl.ID)
	if err != nil {
		return v, err
	}
	earned := map[string]time.Time{}
	for _, b := range badges {
		earned[b.Code] = b.EarnedAt
	}
	for _, ach := range pol.Achievements {
		bv := protocol.ShieldBadgeView{Code: ach.Code, Name: ach.Name, Description: ach.Description}
		if t, ok := earned[ach.Code]; ok {
			bv.EarnedAt = &t
			pv.Badges = append(pv.Badges, bv)
			if now.Sub(t) < 3*24*time.Hour {
				v.Notices = append(v.Notices, protocol.ShieldNotice{ID: "conquista:" + ach.Code, Title: "Nova conquista: " + ach.Name, Text: ach.Description})
			}
		} else if len(pv.Locked) < 3 {
			pv.Locked = append(pv.Locked, bv)
		}
	}
	recent, err := db.ShieldXPOf(ctx, pl.ID, 10)
	if err != nil {
		return v, err
	}
	for _, e := range recent {
		pv.Recent = append(pv.Recent, protocol.ShieldXPView{Reason: e.Reason, Points: e.Points, Day: e.Day})
	}
	v.Player = pv

	ms, err := db.ShieldMissions(ctx, 50)
	if err != nil {
		return v, err
	}
	for _, m := range ms {
		st := missionStatus(m, now)
		if (st != "ativa" && st != "agendada" && st != "concluida") || (m.Ring != nil && *m.Ring != ring) {
			continue
		}
		if st == "concluida" && now.Sub(*m.CompletedAt) > 7*24*time.Hour {
			continue
		}
		mv := protocol.ShieldMissionView{ID: m.ID, Title: m.Title, Description: m.Description, Scope: m.Scope, Status: st, XP: m.XP,
			StartsAt: m.StartsAt, EndsAt: m.EndsAt}
		switch m.Scope {
		case "individual":
			n, err := db.ShieldProtectedDaysOf(ctx, pl.ID, m.StartsAt, m.EndsAt)
			if err != nil {
				return v, err
			}
			mv.Percent = min(100, n*100/max(m.Goal.Days, 1))
			mv.Progress = fmt.Sprintf("%d de %d dias protegidos", min(n, m.Goal.Days), m.Goal.Days)
		default:
			if m.Total > 0 {
				mv.Percent = min(100, m.Done*100/m.Total)
			}
			what := "com o selo 100%"
			if m.Goal.Criterion != "" {
				what = "com “" + m.Goal.Criterion.Label() + "” conforme"
			}
			mv.Progress = fmt.Sprintf("%d%% das máquinas %s (meta: %d%%)", mv.Percent, what, m.Goal.Percent)
		}
		if st == "ativa" {
			v.Notices = append(v.Notices, protocol.ShieldNotice{ID: fmt.Sprintf("missao:%d", m.ID), Title: "Missão: " + m.Title, Text: mv.Progress})
		}
		v.Missions = append(v.Missions, mv)
	}
	if have {
		for _, c := range sc.Result.Criteria {
			if action := shieldAction(c); action != "" && c.Since != nil {
				v.Notices = append(v.Notices, protocol.ShieldNotice{ID: fmt.Sprintf("acao:%s:%d", c.Criterion, c.Since.Unix()),
					Title: "Sua proteção precisa de você", Text: c.Criterion.Label() + ": " + c.Detail + "."})
			}
		}
		if sc.Result.Sealed && now.Sub(sc.ChangedAt) < 24*time.Hour {
			v.Notices = append(v.Notices, protocol.ShieldNotice{ID: fmt.Sprintf("selo:%d", sc.ChangedAt.Unix()), Title: "100% protegido",
				Text: "Este computador está com o selo 100%. " + pol.Brand.Slogan})
		}
	}
	if pol.Ranking.Collective {
		stats, err := db.ShieldRingStats(ctx)
		if err != nil {
			return v, err
		}
		names, err := a.ringNames(ctx)
		if err != nil {
			return v, err
		}
		groups, _ := shieldGroups(&pol, stats, names)
		mine := groupLabel(ring, names)
		for _, g := range groups {
			if len(v.Groups) < 20 || g.Name == mine {
				v.Groups = append(v.Groups, protocol.ShieldGroupView{Pos: g.Pos, Name: g.Name, Machines: g.Machines, Percent: g.Pct, Mine: g.Name == mine})
			}
		}
		if len(v.Groups) > 20 {
			v.Groups = v.Groups[len(v.Groups)-20:]
		}
	}
	if have {
		places, err := a.shieldPlaceViews(ctx, db, &pol, sc.Place)
		if err != nil {
			return v, err
		}
		v.Places = places
	}
	if len(v.Notices) > 20 {
		v.Notices = v.Notices[:20]
	}
	return v, nil
}

// shieldAction: o que a pessoa pode fazer na própria Central para o critério.
func shieldAction(c shield.CriterionResult) string {
	switch {
	case c.Criterion == shield.CritReboot && (c.Status == shield.DentroPrazo || c.Status == shield.AguardandoUsuario):
		return "reiniciar"
	case c.Criterion == shield.CritThirdParty && c.Status == shield.AguardandoUsuario:
		return "atualizar"
	}
	return ""
}

func shieldMachineView(r shield.Result, at time.Time) *protocol.ShieldMachineView {
	m := &protocol.ShieldMachineView{Score: r.Score, Display: r.Display, Sealed: r.Sealed, Overall: string(r.Overall),
		OverallLabel: r.Overall.Label(), Blockers: r.Blockers, EvaluatedAt: at, Criteria: []protocol.ShieldCriterionView{}}
	for _, c := range r.Criteria {
		cv := protocol.ShieldCriterionView{Criterion: string(c.Criterion), Label: c.Criterion.Label(), Status: string(c.Status),
			StatusLabel: c.Status.Label(), Blame: string(c.Blame), Detail: c.Detail, Deadline: c.Deadline, Weight: c.Weight,
			Obtained: c.Obtained, Mandatory: c.Mandatory, Action: shieldAction(c)}
		if c.Blame != shield.BlameNone {
			cv.BlameLabel = c.Blame.Label()
		}
		m.Criteria = append(m.Criteria, cv)
	}
	return m
}

// shieldPlaceViews: os rankings por lugar para a Central — os prédios (os 10 primeiros e o
// desta máquina), e, no prédio desta máquina, os andares e as salas (as 10 primeiras e a dela).
func (a *api) shieldPlaceViews(ctx context.Context, db shieldStore, pol *shield.Policy, me shield.Place) ([]protocol.ShieldPlaceView, error) {
	var out []protocol.ShieldPlaceView
	build := func(kind, title, building, mineKey string, top int) error {
		list, err := db.ShieldRankings(ctx, kind, building, 500)
		if err != nil || len(list) == 0 {
			return err
		}
		pv := protocol.ShieldPlaceView{Kind: kind, Title: title}
		for i, r := range list {
			mine := mineKey != "" && r.Key == mineKey
			if mine {
				pv.Mine, pv.Pos = r.Label, r.Pos
			}
			pv.Of = r.Of
			if i < top || mine {
				pv.Rows = append(pv.Rows, protocol.ShieldGroupView{Pos: r.Pos, Name: r.Label, Machines: r.Machines, Percent: r.Percent(), Mine: mine})
			}
		}
		out = append(out, pv)
		return nil
	}
	if pol.Ranking.Buildings {
		if err := build(shield.RankBuilding, "Prédios", "", shield.PlaceKey(me.Building, ""), 10); err != nil {
			return nil, err
		}
	}
	if me.Building == "" {
		return out, nil
	}
	if pol.Ranking.Floors && me.Floor != "" {
		if err := build(shield.RankFloor, "Andares do "+texto.PlaceName(me.Building), me.Building, shield.PlaceKey(me.Building, me.Floor), 30); err != nil {
			return nil, err
		}
	}
	if pol.Ranking.Rooms && me.Room != "" {
		if err := build(shield.RankRoom, "Salas do "+texto.PlaceName(me.Building), me.Building, shield.PlaceKey(me.Building, me.Room), 10); err != nil {
			return nil, err
		}
	}
	return out, nil
}
