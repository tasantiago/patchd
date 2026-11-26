package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/shield"
)

func shieldDeTeste(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m-a", "m-b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO machines (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	return NewPostgres(pool), ctx
}

func TestShieldPolitica(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	if _, ok, err := st.ShieldPolicy(ctx); err != nil || ok {
		t.Fatalf("sem política gravada: %v %v", ok, err)
	}
	p := shield.Default()
	p.Enabled = true
	v1, err := st.SaveShieldPolicy(ctx, p, "piloto", "admin")
	if err != nil {
		t.Fatal(err)
	}
	p.PilotRings = []int{0}
	v2, err := st.SaveShieldPolicy(ctx, p, "", "admin")
	if err != nil || v2 <= v1 {
		t.Fatalf("versão nova: %d %d %v", v1, v2, err)
	}
	row, ok, err := st.ShieldPolicy(ctx)
	if err != nil || !ok || row.Version != v2 {
		t.Fatalf("%+v %v", row, err)
	}
	got, err := shield.Parse(row.Body)
	if err != nil || !got.Enabled || len(got.PilotRings) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	bad := shield.Default()
	bad.Credits[shield.Desconhecido] = 0.5
	if _, err := st.SaveShieldPolicy(ctx, bad, "", "admin"); err == nil {
		t.Fatal("política inválida gravada")
	}
	if h, _ := st.ShieldPolicyHistory(ctx, 10); len(h) != 2 {
		t.Fatalf("histórico: %d", len(h))
	}
}

func TestShieldPontuacoesEmLote(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	p := shield.Default()
	r := shield.Evaluate(&p, shield.Evidence{}, now)
	list := []ShieldScore{{MachineID: "m-a", PolicyVersion: 1, Result: r, EvaluatedAt: now, ChangedAt: now},
		{MachineID: "m-b", PolicyVersion: 1, Result: r, EvaluatedAt: now, ChangedAt: now}}
	if err := st.SaveShieldScores(ctx, list); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveShieldScores(ctx, list[:1]); err != nil { // a mesma de novo: troca
		t.Fatal(err)
	}
	all, err := st.ShieldScores(ctx)
	if err != nil || len(all) != 2 || all["m-a"].Result.Overall != r.Overall {
		t.Fatalf("%+v %v", all, err)
	}
	if err := st.DeleteShieldScores(ctx, []string{"m-b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ShieldScoreOf(ctx, "m-b"); ok {
		t.Fatal("fora do piloto continuou")
	}
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	score := 80.5
	if err := st.SaveShieldDaily(ctx, []ShieldDaily{{MachineID: "m-a", Day: day, Score: &score, Ring: 1}, {MachineID: "m-a", Day: day, Sealed: true, Ring: 1}}); err != nil {
		t.Fatal(err)
	}
	tr, err := st.ShieldTrends(ctx, day)
	if err != nil || len(tr) != 1 || tr[0].Sealed != 1 || tr[0].Machines != 1 {
		t.Fatalf("o último retrato do dia vale: %+v %v", tr, err)
	}
}

func TestShieldPessoaPeloSID(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids, err := st.ShieldTouchAccounts(ctx, map[string]string{`EXEMPLO\Ana.Souza`: "Ana Souza"})
	if err != nil || ids[`exemplo\ana.souza`] == 0 {
		t.Fatalf("%+v %v", ids, err)
	}
	pl, err := st.ShieldPlayerFor(ctx, `EXEMPLO\ana.souza`, "S-1-5-21-1-2-3-1001", "")
	if err != nil || pl.ID != ids[`exemplo\ana.souza`] || pl.SID == "" || pl.DisplayName != "Ana Souza" {
		t.Fatalf("a mesma pessoa, agora com SID: %+v %v", pl, err)
	}
	// Conta renomeada, mesmo SID: a mesma pessoa.
	pl2, err := st.ShieldPlayerFor(ctx, `EXEMPLO\ana.lima`, "S-1-5-21-1-2-3-1001", "Ana Lima")
	if err != nil || pl2.ID != pl.ID || pl2.Account != `exemplo\ana.lima` {
		t.Fatalf("renomeada: %+v %v", pl2, err)
	}
	// O check-in já tinha visto a conta nova (sem SID): a linha dela solta a conta.
	if _, err := st.ShieldTouchAccounts(ctx, map[string]string{`EXEMPLO\bruno`: ""}); err != nil {
		t.Fatal(err)
	}
	b, err := st.ShieldPlayerFor(ctx, `EXEMPLO\bruno`, "S-1-5-21-1-2-3-1002", "")
	if err != nil || b.ID == pl.ID {
		t.Fatalf("outra pessoa: %+v %v", b, err)
	}
	if _, err := st.ShieldPlayerFor(ctx, "", "", ""); err == nil {
		t.Fatal("identidade vazia aceita")
	}
}

func TestShieldXPIdempotenteComTeto(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`exemplo\ana`: ""})
	id := ids[`exemplo\ana`]
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	p := shield.Default()
	s := shield.Streak{}
	s.Step(day, shield.DayProtected)
	c := ShieldCommit{PlayerID: id, Day: day, Streak: &s, Events: shield.DayXP(&p, day, shield.DayProtected, s), MaxPerDay: 15}
	got, err := st.CommitShield(ctx, c)
	if err != nil || got.Points != 10 {
		t.Fatalf("primeiro: %+v %v", got, err)
	}
	again, err := st.CommitShield(ctx, c)
	if err != nil || again.Points != 0 {
		t.Fatalf("o mesmo dia de novo somou: %+v %v", again, err)
	}
	// O teto do dia: 10 já ganhos, teto 15, evento de 15 vale 5.
	reb := ShieldCommit{PlayerID: id, Day: day, Events: []shield.XPEvent{{Kind: shield.XPReboot, Ref: "m-a:2026-11-24", Points: 15}}, MaxPerDay: 15}
	if got, err := st.CommitShield(ctx, reb); err != nil || got.Points != 5 {
		t.Fatalf("teto: %+v %v", got, err)
	}
	// A sequência não volta: fechar um dia anterior não grava por cima.
	old := shield.Streak{LastDay: "2026-11-01"}
	if _, err := st.CommitShield(ctx, ShieldCommit{PlayerID: id, Day: day, Streak: &old}); err != nil {
		t.Fatal(err)
	}
	pls, _ := st.ShieldPlayersByID(ctx, []int64{id})
	if pls[id].Streak.LastDay != "2026-11-24" || pls[id].XP != 15 {
		t.Fatalf("%+v", pls[id])
	}
	// Conquista: uma vez só, com o XP dela.
	a := shield.DefaultAchievements()[0]
	c2 := ShieldCommit{PlayerID: id, Day: day.AddDate(0, 0, 1), Badges: []shield.Achievement{a}}
	if got, err := st.CommitShield(ctx, c2); err != nil || len(got.Badges) != 1 || got.Points != a.XP {
		t.Fatalf("conquista: %+v %v", got, err)
	}
	if got, _ := st.CommitShield(ctx, c2); len(got.Badges) != 0 || got.Points != 0 {
		t.Fatal("conquista repetida")
	}
	counters, have, err := st.ShieldCounters(ctx, []int64{id})
	if err != nil || !have[id][a.Code] || counters[id][shield.MetricReboots] != 1 || counters[id][shield.MetricProtectedDays] != 1 {
		t.Fatalf("%+v %+v %v", counters, have, err)
	}
}

func TestShieldAjusteDoAdministrador(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`exemplo\ana`: ""})
	id := ids[`exemplo\ana`]
	now := time.Now()
	if _, err := st.AdjustShieldXP(ctx, id, 50, "", "admin", "r1", now); !errors.Is(err, ErrShieldAdjust) {
		t.Fatal("ajuste sem motivo aceito")
	}
	if ok, err := st.AdjustShieldXP(ctx, id, 50, "chamado 123: XP perdido na queda do servidor", "admin", "r1", now); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, _ := st.AdjustShieldXP(ctx, id, 50, "reenvio", "admin", "r1", now); ok {
		t.Fatal("o reenvio do formulário somou de novo")
	}
	if ok, _ := st.AdjustShieldXP(ctx, id, -100, "correção", "admin", "r2", now); !ok {
		t.Fatal("negativo")
	}
	pls, _ := st.ShieldPlayersByID(ctx, []int64{id})
	if pls[id].XP != 0 {
		t.Fatalf("XP não fica negativo: %d", pls[id].XP)
	}
	ev, _ := st.ShieldXPOf(ctx, id, 10)
	if len(ev) != 2 || ev[0].Actor != "admin" {
		t.Fatalf("%+v", ev)
	}
}

func TestShieldPresencaEDia(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`exemplo\ana`: "", `exemplo\bruno`: ""})
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	pres := []ShieldPresence{{ids[`exemplo\ana`], "m-a", day}, {ids[`exemplo\bruno`], "m-a", day}, {ids[`exemplo\ana`], "m-b", day},
		{ids[`exemplo\ana`], "m-a", day}, {ids[`exemplo\ana`], "nao-existe", day}}
	if err := st.AddShieldPresence(ctx, pres); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveShieldDaily(ctx, []ShieldDaily{{MachineID: "m-a", Day: day, Sealed: true}, {MachineID: "m-b", Day: day}}); err != nil {
		t.Fatal(err)
	}
	in, err := st.ShieldDayInputs(ctx, day)
	if err != nil || len(in[ids[`exemplo\ana`]]) != 2 || len(in[ids[`exemplo\bruno`]]) != 1 {
		t.Fatalf("máquina compartilhada conta para os dois: %+v %v", in, err)
	}
	on, _ := st.ShieldPresentOn(ctx, day)
	if len(on["m-a"]) != 2 {
		t.Fatalf("%+v", on)
	}
	mine, _ := st.ShieldMachinesOf(ctx, ids[`exemplo\ana`], day)
	if len(mine) != 2 {
		t.Fatalf("%+v", mine)
	}
	if n, err := st.PruneShield(ctx, day.AddDate(0, 0, 1)); err != nil || n != 5 {
		t.Fatalf("retenção: %d %v", n, err)
	}
}

func TestShieldExcecoesEMissoes(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	now := time.Now().UTC()
	if _, err := st.AddShieldException(ctx, ShieldException{MachineID: "m-a", Criterion: "x", Reason: "r", ExpiresAt: now.Add(time.Hour), CreatedBy: "admin"}, now); !errors.Is(err, ErrShieldException) {
		t.Fatal("critério desconhecido aceito")
	}
	if _, err := st.AddShieldException(ctx, ShieldException{MachineID: "m-a", Criterion: shield.CritThirdParty, Reason: "r", ExpiresAt: now.Add(400 * 24 * time.Hour), CreatedBy: "admin"}, now); !errors.Is(err, ErrShieldException) {
		t.Fatal("validade longa demais aceita")
	}
	if _, err := st.AddShieldException(ctx, ShieldException{MachineID: "nao-existe", Criterion: shield.CritOS, Reason: "r", ExpiresAt: now.Add(time.Hour), CreatedBy: "admin"}, now); !errors.Is(err, ErrShieldNotFound) {
		t.Fatal("máquina inexistente")
	}
	id, err := st.AddShieldException(ctx, ShieldException{MachineID: "m-a", Criterion: shield.CritThirdParty, Reason: "programa legado homologado", ExpiresAt: now.Add(24 * time.Hour), CreatedBy: "admin"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := st.ShieldExceptions(ctx, false, now); len(l) != 1 {
		t.Fatal("vigente")
	}
	if l, _ := st.ShieldExceptions(ctx, false, now.Add(48*time.Hour)); len(l) != 0 {
		t.Fatal("vencida continua valendo")
	}
	if _, ok, _ := st.RevokeShieldException(ctx, id, "admin"); !ok {
		t.Fatal("revogar")
	}
	if _, ok, _ := st.RevokeShieldException(ctx, id, "admin"); ok {
		t.Fatal("revogar duas vezes")
	}

	m := ShieldMission{Title: "Semana do reinício", Scope: "coletiva", Goal: MissionGoal{Percent: 90, Criterion: shield.CritReboot},
		StartsAt: now, EndsAt: now.Add(7 * 24 * time.Hour), State: "rascunho", XP: 50, CreatedBy: "admin"}
	mid, err := st.SaveShieldMission(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	bad := m
	bad.Goal = MissionGoal{Days: 3}
	if _, err := st.SaveShieldMission(ctx, bad); !errors.Is(err, ErrShieldMission) {
		t.Fatal("coletiva com meta de dias aceita")
	}
	if ok, _ := st.SetShieldMissionState(ctx, mid, "publicada"); !ok {
		t.Fatal("publicar")
	}
	m.ID, m.Title = mid, "outro"
	if _, err := st.SaveShieldMission(ctx, m); !errors.Is(err, ErrShieldNotFound) {
		t.Fatal("publicada não se edita")
	}
	if ok, _ := st.CompleteShieldMission(ctx, mid, now); !ok {
		t.Fatal("concluir")
	}
	if ok, _ := st.CompleteShieldMission(ctx, mid, now); ok {
		t.Fatal("concluir duas vezes")
	}
	l, _ := st.ShieldMissions(ctx, 10)
	if len(l) != 1 || l[0].CompletedAt == nil || l[0].Goal.Criterion != shield.CritReboot {
		t.Fatalf("%+v", l)
	}
}

// Zerar: XP, sequência, conquistas e livro voltam a zero; um dia até o zerar não grava mais
// nada (o servidor que refaz os dias na partida não devolve os pontos); depois dele, conta.
func TestShieldZerarPontuacao(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`exemplo\ana`: "", `exemplo\rui`: ""})
	ana := ids[`exemplo\ana`]
	p := shield.Default()
	d1 := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := shield.Streak{}
	s.Step(d1, shield.DayProtected)
	a := shield.DefaultAchievements()[0]
	if got, err := st.CommitShield(ctx, ShieldCommit{PlayerID: ana, Day: d1, Streak: &s, Events: shield.DayXP(&p, d1, shield.DayProtected, s), Badges: []shield.Achievement{a}}); err != nil || got.Points == 0 {
		t.Fatalf("antes: %+v %v", got, err)
	}
	if _, ok, err := st.ShieldLastReset(ctx); ok || err != nil {
		t.Fatalf("nunca zerado: %v %v", ok, err)
	}
	if _, err := st.ResetShieldPoints(ctx, d1, " ", "admin"); !errors.Is(err, ErrShieldReset) {
		t.Fatal("zerar sem motivo aceito")
	}
	cut := d1.AddDate(0, 0, 2) // 22/11: o último dia que fica para trás
	r, err := st.ResetShieldPoints(ctx, cut, "início do programa", "admin")
	if err != nil || r.Players != 2 || r.XP == 0 || r.ID == 0 {
		t.Fatalf("zerar: %+v %v", r, err)
	}
	pls, _ := st.ShieldPlayersByID(ctx, []int64{ana})
	if pl := pls[ana]; pl.XP != 0 || pl.Streak.Protected != 0 || pl.Streak.Best != 0 || pl.Streak.LastDay != "2026-11-22" {
		t.Fatalf("depois de zerar: %+v", pl)
	}
	if b, _ := st.ShieldBadgesOf(ctx, ana); len(b) != 0 {
		t.Fatalf("conquistas: %+v", b)
	}
	if ev, _ := st.ShieldXPOf(ctx, ana, 10); len(ev) != 0 {
		t.Fatalf("livro do XP: %+v", ev)
	}
	// Refazer um dia até o zerar (21 e 22): nada; nem a conquista.
	for _, d := range []time.Time{d1.AddDate(0, 0, 1), cut} {
		s2 := shield.Streak{}
		s2.Step(d, shield.DayProtected)
		got, err := st.CommitShield(ctx, ShieldCommit{PlayerID: ana, Day: d, Streak: &s2, Events: shield.DayXP(&p, d, shield.DayProtected, s2), Badges: []shield.Achievement{a}})
		if err != nil || got.Points != 0 || len(got.Badges) != 0 {
			t.Fatalf("dia %s refeito depois do zerar: %+v %v", d.Format(time.DateOnly), got, err)
		}
	}
	// O dia seguinte conta, a partir do zero.
	next := cut.AddDate(0, 0, 1)
	pls, _ = st.ShieldPlayersByID(ctx, []int64{ana})
	s3 := pls[ana].Streak
	if !s3.Step(next, shield.DayProtected) {
		t.Fatal("o dia seguinte já estava fechado")
	}
	if got, err := st.CommitShield(ctx, ShieldCommit{PlayerID: ana, Day: next, Streak: &s3, Events: shield.DayXP(&p, next, shield.DayProtected, s3)}); err != nil || got.Points == 0 {
		t.Fatalf("depois do zerar: %+v %v", got, err)
	}
	pls, _ = st.ShieldPlayersByID(ctx, []int64{ana})
	if pl := pls[ana]; pl.Streak.Protected != 1 || pl.Streak.Current != 1 || pl.XP == 0 {
		t.Fatalf("recomeço: %+v", pl)
	}
	// Nunca para trás; o mesmo dia de novo pode.
	if _, err := st.ResetShieldPoints(ctx, d1, "voltar", "admin"); !errors.Is(err, ErrShieldReset) {
		t.Fatal("zerar para trás aceito")
	}
	if _, err := st.ResetShieldPoints(ctx, cut, "de novo", "admin"); err != nil {
		t.Fatal(err)
	}
	last, ok, err := st.ShieldLastReset(ctx)
	if err != nil || !ok || last.Reason != "de novo" || last.Day.Format(time.DateOnly) != "2026-11-22" || last.By != "admin" {
		t.Fatalf("último zerar: %+v %v %v", last, ok, err)
	}
}

// Grupos de competição: um prédio em um grupo só, o grupo segue o ID do prédio (renomeado no
// patrimônio continua no grupo), nome único, e excluir solta os prédios.
func TestShieldGruposDeCompeticao(t *testing.T) {
	st, ctx := shieldDeTeste(t)
	ids := map[string]int64{}
	for _, n := range []string{"PREDIO SEDE", "FORUM GERAL", "ANEXO NORTE"} {
		var id int64
		if err := st.pool.QueryRow(ctx, `INSERT INTO buildings (name) VALUES ($1) RETURNING id`, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[n] = id
	}
	g := ShieldCompetition{Name: "  Fóruns   do interior ", Kind: CompetitionBuildings}
	if _, err := st.SaveShieldCompetition(ctx, g, []int64{ids["PREDIO SEDE"]}, "admin"); !IsShieldCompetitionError(err) {
		t.Fatalf("grupo de prédios com um prédio só: %v", err)
	}
	if _, err := st.SaveShieldCompetition(ctx, g, []int64{ids["PREDIO SEDE"], 999999}, "admin"); !IsShieldCompetitionError(err) {
		t.Fatalf("prédio desconhecido: %v", err)
	}
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{Name: "x", Kind: CompetitionFloors}, []int64{ids["PREDIO SEDE"], ids["FORUM GERAL"]}, "admin"); !IsShieldCompetitionError(err) {
		t.Fatalf("grupo por andar com dois prédios: %v", err)
	}
	gid, err := st.SaveShieldCompetition(ctx, g, []int64{ids["PREDIO SEDE"], ids["FORUM GERAL"], ids["FORUM GERAL"]}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// O mesmo prédio em outro grupo: recusa com os nomes; nome repetido: recusa.
	err = func() error {
		_, err := st.SaveShieldCompetition(ctx, ShieldCompetition{Name: "Anexos", Kind: CompetitionFloors}, []int64{ids["PREDIO SEDE"]}, "rui")
		return err
	}()
	if !IsShieldCompetitionError(err) || !strings.Contains(err.Error(), "PREDIO SEDE (no grupo Fóruns do interior)") {
		t.Fatalf("prédio em dois grupos: %v", err)
	}
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{Name: "Fóruns do interior", Kind: CompetitionFloors}, []int64{ids["ANEXO NORTE"]}, "rui"); !IsShieldCompetitionError(err) {
		t.Fatalf("nome repetido: %v", err)
	}
	aid, err := st.SaveShieldCompetition(ctx, ShieldCompetition{Name: "Anexo", Kind: CompetitionFloors}, []int64{ids["ANEXO NORTE"]}, "rui")
	if err != nil {
		t.Fatal(err)
	}
	// Renomeado no patrimônio: continua no grupo, com o nome novo.
	if _, err := st.pool.Exec(ctx, `UPDATE buildings SET name = 'FORUM GERAL - BLOCO A' WHERE id = $1`, ids["FORUM GERAL"]); err != nil {
		t.Fatal(err)
	}
	list, err := st.ShieldCompetitions(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Anexo" || list[1].Name != "Fóruns do interior" || len(list[1].Buildings) != 2 ||
		list[1].Buildings[0].Name != "FORUM GERAL - BLOCO A" || list[1].UpdatedBy != "admin" {
		t.Fatalf("grupos: %+v %v", list, err)
	}
	// Trocar os prédios do grupo: o que saiu fica livre para outro.
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{ID: gid, Name: "Fóruns", Kind: CompetitionBuildings}, []int64{ids["FORUM GERAL"], ids["ANEXO NORTE"]}, "ana"); !IsShieldCompetitionError(err) {
		t.Fatalf("o anexo já está no grupo por andar: %v", err)
	}
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{ID: aid, Name: "Anexo", Kind: CompetitionFloors}, []int64{ids["PREDIO SEDE"]}, "ana"); !IsShieldCompetitionError(err) {
		t.Fatalf("a sede continua no outro grupo: %v", err)
	}
	if name, ok, err := st.DeleteShieldCompetition(ctx, aid); err != nil || !ok || name != "Anexo" {
		t.Fatalf("excluir: %q %v %v", name, ok, err)
	}
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{ID: gid, Name: "Fóruns", Kind: CompetitionBuildings}, []int64{ids["FORUM GERAL"], ids["ANEXO NORTE"]}, "ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveShieldCompetition(ctx, ShieldCompetition{ID: 424242, Name: "Outro", Kind: CompetitionBuildings}, []int64{ids["PREDIO SEDE"], 1}, "ana"); err == nil {
		t.Fatal("grupo inexistente alterado")
	}
	if _, ok, _ := st.DeleteShieldCompetition(ctx, aid); ok {
		t.Fatal("excluído duas vezes")
	}
}
