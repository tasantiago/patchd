package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/version"
)

// PatchD Shield (docs/shield.md): o avaliador periódico. A cada shieldEvery, um servidor (o
// que pega a trava consultiva) recalcula a pontuação de todas as máquinas do Shield a partir
// do que o servidor já sabe (a avaliação de compliance em cache, o inventário, a padronização,
// a versão do agente) e grava só o que mudou, em lote. Na virada do dia (no fuso da política),
// fecha o dia anterior: sequência, XP, conquistas e missões. Tudo idempotente: dois servidores,
// ou o mesmo dia fechado duas vezes, não somam duas vezes (chaves únicas no banco).

// shieldStore é o que o Shield usa do banco (só o PostgreSQL tem).
type shieldStore interface {
	WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error)
	ShieldPolicy(ctx context.Context) (store.ShieldPolicyRow, bool, error)
	ShieldPolicyHistory(ctx context.Context, limit int) ([]store.ShieldPolicyRow, error)
	SaveShieldPolicy(ctx context.Context, p shield.Policy, note, by string) (int, error)
	ShieldScores(ctx context.Context) (map[string]store.ShieldScore, error)
	ShieldScoreOf(ctx context.Context, id string) (store.ShieldScore, bool, error)
	SaveShieldScores(ctx context.Context, list []store.ShieldScore) error
	DeleteShieldScores(ctx context.Context, ids []string) error
	SaveShieldDaily(ctx context.Context, list []store.ShieldDaily) error
	ShieldTrends(ctx context.Context, since time.Time) ([]store.ShieldTrend, error)
	ShieldMachines(ctx context.Context) ([]store.ShieldMachine, error)
	StandardizationSummaries(ctx context.Context) (map[string]store.StandardizationSummary, error)
	ShieldPlayerFor(ctx context.Context, account, sid, display string) (store.ShieldPlayer, error)
	ShieldTouchAccounts(ctx context.Context, names map[string]string) (map[string]int64, error)
	AddShieldPresence(ctx context.Context, list []store.ShieldPresence) error
	ShieldPresentOn(ctx context.Context, day time.Time) (map[string][]int64, error)
	ShieldDayInputs(ctx context.Context, day time.Time) (map[int64][]shield.MachineDay, error)
	ShieldPlayersByID(ctx context.Context, ids []int64) (map[int64]store.ShieldPlayer, error)
	ShieldPlayerByAccount(ctx context.Context, account string) (store.ShieldPlayer, bool, error)
	CommitShield(ctx context.Context, c store.ShieldCommit) (store.ShieldCommitted, error)
	AdjustShieldXP(ctx context.Context, playerID int64, points int, reason, actor, ref string, now time.Time) (bool, error)
	ShieldXPOf(ctx context.Context, playerID int64, limit int) ([]store.ShieldXPEntry, error)
	ShieldCounters(ctx context.Context, ids []int64) (map[int64]shield.Counters, map[int64]map[string]bool, error)
	ShieldBadgesOf(ctx context.Context, playerID int64) ([]store.ShieldBadge, error)
	ShieldMachinesOf(ctx context.Context, playerID int64, since time.Time) ([]string, error)
	ShieldLeaders(ctx context.Context, since time.Time, limit int) ([]store.ShieldLeader, error)
	ShieldPlayerList(ctx context.Context, q string, limit, offset int) ([]store.ShieldPlayer, int, error)
	AddShieldException(ctx context.Context, e store.ShieldException, now time.Time) (int64, error)
	RevokeShieldException(ctx context.Context, id int64, by string) (store.ShieldException, bool, error)
	ShieldExceptions(ctx context.Context, all bool, now time.Time) ([]store.ShieldException, error)
	ShieldMissions(ctx context.Context, limit int) ([]store.ShieldMission, error)
	SaveShieldMission(ctx context.Context, m store.ShieldMission) (int64, error)
	SetShieldMissionState(ctx context.Context, id int64, state string) (bool, error)
	CompleteShieldMission(ctx context.Context, id int64, at time.Time) (bool, error)
	SetShieldMissionProgress(ctx context.Context, id int64, done, total int) error
	ShieldProtectedDaysOf(ctx context.Context, playerID int64, from, to time.Time) (int, error)
	MachineRing(ctx context.Context, id string) (int, bool, error)
	ShieldRingStats(ctx context.Context) ([]store.ShieldRingStat, error)
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
	ReplaceShieldRankings(ctx context.Context, list []store.ShieldRank) error
	ShieldRankings(ctx context.Context, kind, building string, limit int) ([]store.ShieldRank, error)
	ShieldRankOf(ctx context.Context, kind, key string) (store.ShieldRank, bool, error)
	ShieldProtectedDaysBetween(ctx context.Context, from, to time.Time) (map[int64]int, error)
	ShieldPlayersInRings(ctx context.Context, ring *int, from, to time.Time) ([]int64, error)
	PruneShield(ctx context.Context, before time.Time) (int64, error)
}

var _ shieldStore = (*store.Postgres)(nil)

const (
	shieldEvery     = 5 * time.Minute      // o recálculo
	shieldPolicyTTL = 30 * time.Second     // o cache da política
	shieldRefresh   = time.Hour            // reescreve a pontuação que não mudou (o "avaliada em")
	shieldCatchUp   = 7                    // dias para trás que o fechamento confere na partida
	shieldRetention = 400 * 24 * time.Hour // retratos e presenças
	shieldRunMax    = 4 * time.Minute      // prazo de uma passada
)

// errShieldOff: sem PostgreSQL, o Shield não existe neste servidor.
var errShieldOff = errors.New("o Shield precisa do PostgreSQL")

// shieldState é o estado do Shield na memória deste servidor.
type shieldState struct {
	mu       sync.Mutex
	pol      shield.Policy
	version  int
	polAt    time.Time
	closed   string                       // o último dia fechado por este servidor
	rankSum  string                       // o resumo dos rankings por lugar gravados (só regrava o que mudou)
	daily    map[string]store.ShieldDaily // o último retrato gravado hoje, por máquina
	lastRun  time.Time
	lastErr  string
	counts   map[shield.Overall]int // da última passada, para as métricas e o painel
	duration time.Duration
}

func (a *api) shieldDB() (shieldStore, bool) {
	s, ok := a.store.(shieldStore)
	return s, ok
}

// shieldPolicy devolve a política em vigor (do cache curto) e a versão (0: a padrão).
func (a *api) shieldPolicy(ctx context.Context) (shield.Policy, int, error) {
	db, ok := a.shieldDB()
	if !ok {
		return shield.Default(), 0, errShieldOff
	}
	st := &a.shield
	st.mu.Lock()
	defer st.mu.Unlock()
	if now := a.now(); !st.polAt.IsZero() && now.Sub(st.polAt) < shieldPolicyTTL && !now.Before(st.polAt) {
		return st.pol, st.version, nil
	}
	row, found, err := db.ShieldPolicy(ctx)
	if err != nil {
		if !st.polAt.IsZero() {
			return st.pol, st.version, nil // o banco piscou: vale a última lida
		}
		return shield.Default(), 0, err
	}
	pol, ver := shield.Default(), 0
	if found {
		p, err := shield.Parse(row.Body)
		if err != nil {
			// Gravada por uma versão mais nova (ou corrompida): o Shield fica desligado, e o log diz.
			a.logger.Error("política do Shield inválida: módulo desligado até corrigir", "versao", row.Version, "error", err)
			p = shield.Default()
		}
		pol, ver = p, row.Version
	}
	st.pol, st.version, st.polAt = pol, ver, a.now()
	return pol, ver, nil
}

// shieldPolicyStale faz a próxima leitura ir ao banco (depois de gravar).
func (a *api) shieldPolicyStale() {
	a.shield.mu.Lock()
	a.shield.polAt = time.Time{}
	a.shield.mu.Unlock()
}

// shieldLoop roda o avaliador até o ctx acabar.
func (a *api) shieldLoop(ctx context.Context) {
	db, ok := a.shieldDB()
	if !ok || a.compliance == nil {
		return
	}
	t := time.NewTicker(shieldEvery)
	defer t.Stop()
	first := time.NewTimer(30 * time.Second) // deixa o cache de compliance esquentar
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, shieldRunMax)
		ran, err := db.WithAdvisoryLock(rctx, store.LockShield, func(ctx context.Context) error { return a.shieldRun(ctx, db, a.now()) })
		cancel()
		if err != nil && ctx.Err() == nil {
			a.logger.Warn("Shield: avaliação", "error", err)
		}
		if !ran {
			a.logger.Debug("Shield: outro servidor está avaliando")
		}
	}
}

// shieldInputs é o que todas as máquinas precisam, lido de uma vez (sem N+1).
type shieldInputs struct {
	machines   []store.ShieldMachine
	summary    map[string]protocol.MachineSummary
	status     map[string]protocol.ComplianceStatus
	items      map[string]protocol.MachineItems
	std        map[string]store.StandardizationSummary
	exceptions map[string]map[shield.Criterion]bool
	offers     map[string]bool // programas que a Central oferece para atualizar
	published  string
	places     map[string]shield.Place // o lugar de cada máquina (rankings por lugar)
	floorStart map[string]uint32       // o começo de cada faixa (a ordem dos andares sem nome)
}

func (a *api) shieldGather(ctx context.Context, db shieldStore, now time.Time) (shieldInputs, error) {
	var in shieldInputs
	var err error
	if in.machines, err = db.ShieldMachines(ctx); err != nil {
		return in, err
	}
	list, err := a.store.Machines(ctx)
	if err != nil {
		return in, err
	}
	in.summary = make(map[string]protocol.MachineSummary, len(list))
	for _, m := range list {
		in.summary[m.ID] = m
	}
	fleet, err := a.compliance.Fleet(ctx)
	if err != nil {
		return in, fmt.Errorf("compliance: %w", err)
	}
	in.status = make(map[string]protocol.ComplianceStatus, len(fleet.Machines))
	for _, s := range fleet.Machines {
		in.status[s.MachineID] = s
	}
	items, err := a.complianceItems(ctx)
	if err != nil {
		return in, fmt.Errorf("pendências: %w", err)
	}
	in.items = make(map[string]protocol.MachineItems, len(items))
	for _, it := range items {
		in.items[it.MachineID] = it
	}
	if in.std, err = db.StandardizationSummaries(ctx); err != nil {
		return in, err
	}
	exc, err := db.ShieldExceptions(ctx, false, now)
	if err != nil {
		return in, err
	}
	in.exceptions = map[string]map[shield.Criterion]bool{}
	for _, e := range exc {
		if in.exceptions[e.MachineID] == nil {
			in.exceptions[e.MachineID] = map[shield.Criterion]bool{}
		}
		in.exceptions[e.MachineID][e.Criterion] = true
	}
	offers, err := a.store.SelfServiceOffers(ctx)
	if err != nil {
		return in, err
	}
	in.offers = map[string]bool{}
	for _, o := range offers {
		if o.CanUpdate && !o.NeedsApproval {
			in.offers[o.AppID] = true
		}
	}
	in.published, _ = a.publishedVersion()
	if err := a.shieldPlaces(ctx, &in); err != nil {
		return in, fmt.Errorf("lugares: %w", err)
	}
	return in, nil
}

// shieldEvidenceFor monta a evidência de uma máquina.
func shieldEvidenceFor(in *shieldInputs, id string, prev map[shield.Criterion]time.Time, now time.Time) shield.Evidence {
	e := shield.Evidence{Since: prev, Exceptions: in.exceptions[id]}
	sum := in.summary[id]
	st, evaluated := in.status[id]
	e.LastSeen = sum.LastSeenAt
	e.Stale = !sum.LastSeenAt.IsZero() && now.Sub(sum.LastSeenAt) > compliance.StaleAfter
	e.Reboot = sum.RebootPending
	if evaluated {
		e.InventoryAt = st.InventoryCollectedAt
		switch compliance.State(st.State) {
		case compliance.StateOK, compliance.StateMissing, compliance.StateReboot:
			e.OSEvaluated = true
		case compliance.StateStale:
			e.OSEvaluated = st.Catalog != ""
		}
		e.OSPending = st.Pending
		e.OSCritical = st.Exploited
	}
	if e.InventoryAt == nil && sum.InventoryChangedAt != nil {
		e.InventoryAt = sum.InventoryChangedAt
	}
	if it, ok := in.items[id]; ok {
		for _, p := range it.Items {
			if !p.Exploited && strings.EqualFold(p.Severity, "critical") {
				e.OSCritical++
			}
		}
		for _, tp := range it.ThirdParty {
			switch strings.ToUpper(tp.State) {
			case "DESATUALIZADO":
				e.TPOutdated++
				if in.offers[tp.AppID] {
					e.TPUserFixable++
				}
			case "FORA DE SUPORTE":
				e.TPUnsupported++
			}
		}
	}
	e.TPKnown = e.InventoryAt != nil
	if s, ok := in.std[id]; ok {
		e.StdApplies, e.StdKnown = s.Applies, s.Known
		e.StdFailed, e.StdWaiting, e.StdConflicts = s.Failed, s.Waiting, s.Conflicts
	}
	if in.published != "" {
		e.AgentKnown = sum.AgentVersion != ""
		c, err := version.CompareSemver(sum.AgentVersion, in.published)
		out := err == nil && c < 0
		e.AgentOutdated = &out
		if err != nil {
			e.AgentKnown = false
		}
	}
	return e
}

// shieldRun é uma passada do avaliador.
func (a *api) shieldRun(ctx context.Context, db shieldStore, now time.Time) error {
	start := time.Now()
	pol, ver, err := a.shieldPolicy(ctx)
	if err != nil {
		return err
	}
	if !pol.Enabled {
		a.shieldDone(start, nil, nil)
		return nil
	}
	loc := pol.Location(a.panelLocation())
	today := dateOf(now, loc)
	in, err := a.shieldGather(ctx, db, now)
	if err != nil {
		a.shieldDone(start, nil, err)
		return err
	}
	prev, err := db.ShieldScores(ctx)
	if err != nil {
		return err
	}
	var writes []store.ShieldScore
	var gone []string
	var daily []store.ShieldDaily
	counts := map[shield.Overall]int{}
	rebooted := []string{}
	names := map[string]string{}
	seen := map[string][]string{} // máquina → contas conectadas
	a.shield.mu.Lock()
	if a.shield.daily == nil {
		a.shield.daily = map[string]store.ShieldDaily{}
	}
	lastDaily := a.shield.daily
	a.shield.mu.Unlock()
	nextDaily := map[string]store.ShieldDaily{}
	var placed []shield.PlaceScore
	for _, m := range in.machines {
		old, had := prev[m.ID]
		if !pol.InScope(m.Ring) {
			if had {
				gone = append(gone, m.ID)
			}
			continue
		}
		var since map[shield.Criterion]time.Time
		if had {
			since = old.Result.SinceMap()
		}
		res := shield.Evaluate(&pol, shieldEvidenceFor(&in, m.ID, since, now), now)
		counts[res.Overall]++
		place := in.places[m.ID]
		placed = append(placed, shield.PlaceScore{Place: place, Sealed: res.Sealed, Score: res.Score})
		s := store.ShieldScore{MachineID: m.ID, PolicyVersion: ver, Place: place, Result: res, EvaluatedAt: now, ChangedAt: now}
		if had {
			same := sameResult(old.Result, res)
			if same {
				s.ChangedAt = old.ChangedAt
			}
			if same && old.Place == place && old.PolicyVersion == ver && now.Sub(old.EvaluatedAt) < shieldRefresh {
				s = old // nada mudou: não grava
			} else {
				writes = append(writes, s)
			}
			if rebootCleared(old.Result, res) {
				rebooted = append(rebooted, m.ID)
			}
		} else {
			writes = append(writes, s)
		}
		d := store.ShieldDaily{MachineID: m.ID, Day: today, Score: res.Score, Sealed: res.Sealed, UserFault: res.UserFault,
			NoData: res.Overall == shield.NoData, Ring: m.Ring, PolicyVersion: ver}
		nextDaily[m.ID] = d
		if l, ok := lastDaily[m.ID]; !ok || !sameDaily(l, d) {
			daily = append(daily, d)
		}
		for _, u := range m.Users {
			if acc := store.NormalizeAccount(u.Account); u.Active && playable(acc) {
				if names[acc] == "" {
					names[acc] = u.DisplayName
				}
				seen[m.ID] = append(seen[m.ID], acc)
			}
		}
	}
	if err := db.SaveShieldScores(ctx, writes); err != nil {
		return err
	}
	if err := db.DeleteShieldScores(ctx, gone); err != nil {
		return err
	}
	if err := db.SaveShieldDaily(ctx, daily); err != nil {
		return err
	}
	a.shield.mu.Lock()
	a.shield.daily = nextDaily
	a.shield.mu.Unlock()
	// Presença: quem está com sessão ativa nas máquinas do Shield.
	ids, err := db.ShieldTouchAccounts(ctx, names)
	if err != nil {
		return err
	}
	var pres []store.ShieldPresence
	for mid, accs := range seen {
		for _, acc := range accs {
			if id, ok := ids[acc]; ok {
				pres = append(pres, store.ShieldPresence{PlayerID: id, MachineID: mid, Day: today})
			}
		}
	}
	if err := db.AddShieldPresence(ctx, pres); err != nil {
		return err
	}
	if err := a.shieldSaveRankings(ctx, db, &pol, in, placed); err != nil {
		return err
	}
	if err := a.shieldRebootXP(ctx, db, &pol, rebooted, today); err != nil {
		return err
	}
	if err := a.shieldUpdateXP(ctx, db, &pol, in, now, loc); err != nil {
		return err
	}
	if err := a.shieldCollective(ctx, db, &pol, in, now); err != nil {
		return err
	}
	if err := a.shieldCloseDays(ctx, db, &pol, today); err != nil {
		return err
	}
	a.shieldDone(start, counts, nil)
	return nil
}

func (a *api) shieldDone(start time.Time, counts map[shield.Overall]int, err error) {
	a.shield.mu.Lock()
	defer a.shield.mu.Unlock()
	a.shield.lastRun, a.shield.duration = a.now(), time.Since(start)
	a.shield.lastErr = ""
	if err != nil {
		a.shield.lastErr = err.Error()
	}
	if counts != nil || err == nil {
		a.shield.counts = counts
	}
	var bands map[string]int
	if counts != nil {
		bands = map[string]int{}
		for o, n := range counts {
			bands[string(o)] = n
		}
	}
	a.metrics.shieldObserve(bands, a.shield.duration, err != nil)
}

// playable: contas de gente (fora as de serviço e as vazias).
func playable(acc string) bool {
	if acc == "" || len(acc) > 200 {
		return false
	}
	user := acc[strings.LastIndexByte(acc, '\\')+1:]
	switch user {
	case "system", "sistema", "local service", "network service", "root", "administrator", "administrador":
		return false
	}
	return !strings.HasSuffix(user, "$")
}

// dateOf: a data (meia-noite UTC do dia local), para as colunas date.
func dateOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sameResult: a mesma avaliação (sem contar o relógio dos prazos já gravados).
func sameResult(a, b shield.Result) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func sameDaily(a, b store.ShieldDaily) bool {
	eq := (a.Score == nil) == (b.Score == nil) && (a.Score == nil || *a.Score == *b.Score)
	return eq && a.Day.Equal(b.Day) && a.Sealed == b.Sealed && a.UserFault == b.UserFault && a.NoData == b.NoData &&
		a.Ring == b.Ring && a.PolicyVersion == b.PolicyVersion
}

// rebootCleared: havia reinício pendente e agora não há (alguém reiniciou).
func rebootCleared(old, cur shield.Result) bool {
	var was, now shield.Status
	for _, c := range old.Criteria {
		if c.Criterion == shield.CritReboot {
			was = c.Status
		}
	}
	for _, c := range cur.Criteria {
		if c.Criterion == shield.CritReboot {
			now = c.Status
		}
	}
	return (was == shield.DentroPrazo || was == shield.AguardandoUsuario) && now == shield.Conforme
}

// shieldRebootXP: quem estava na máquina hoje ganha o XP do reinício (uma vez por máquina e dia).
func (a *api) shieldRebootXP(ctx context.Context, db shieldStore, pol *shield.Policy, machines []string, today time.Time) error {
	if len(machines) == 0 || pol.XP.Reboot <= 0 {
		return nil
	}
	present, err := db.ShieldPresentOn(ctx, today)
	if err != nil {
		return err
	}
	for _, mid := range machines {
		for _, pid := range present[mid] {
			ev := shield.XPEvent{Kind: shield.XPReboot, Ref: mid + ":" + today.Format(time.DateOnly), Points: pol.XP.Reboot,
				Reason: "reiniciou quando a atualização pediu"}
			if _, err := db.CommitShield(ctx, store.ShieldCommit{PlayerID: pid, Day: today, Events: []shield.XPEvent{ev}, MaxPerDay: pol.XP.MaxPerDay}); err != nil {
				return err
			}
		}
	}
	return nil
}

// shieldUpdateXP: os programas que a pessoa atualizou ou instalou pela Central (jobs do
// autoatendimento concluídos) nas máquinas do Shield.
func (a *api) shieldUpdateXP(ctx context.Context, db shieldStore, pol *shield.Policy, in shieldInputs, now time.Time, loc *time.Location) error {
	if pol.XP.Update <= 0 {
		return nil
	}
	list, err := a.store.SelfServiceJobs(ctx, now.Add(-48*time.Hour), 2000)
	if err != nil {
		return err
	}
	ring := map[string]int{}
	for _, m := range in.machines {
		ring[m.ID] = m.Ring
	}
	type got struct {
		acc string
		job store.JobRow
	}
	var done []got
	names := map[string]string{}
	for _, j := range list {
		r, ok := ring[j.MachineID]
		if j.Type != jobs.TypeThirdParty || j.State != jobs.StateDone || j.FinishedAt == nil || !ok || !pol.InScope(r) {
			continue
		}
		acc := store.NormalizeAccount(strings.TrimPrefix(j.CreatedBy, store.SelfServiceAuthorPrefix))
		if !playable(acc) {
			continue
		}
		names[acc] = ""
		done = append(done, got{acc, j})
	}
	if len(done) == 0 {
		return nil
	}
	ids, err := db.ShieldTouchAccounts(ctx, names)
	if err != nil {
		return err
	}
	for _, d := range done {
		day := dateOf(*d.job.FinishedAt, loc)
		ev := shield.XPEvent{Kind: shield.XPUpdate, Ref: fmt.Sprintf("job:%d", d.job.ID), Points: pol.XP.Update, Reason: "programa atualizado pela Central"}
		if _, err := db.CommitShield(ctx, store.ShieldCommit{PlayerID: ids[d.acc], Day: day, Events: []shield.XPEvent{ev}, MaxPerDay: pol.XP.MaxPerDay}); err != nil {
			return err
		}
	}
	return nil
}

// missionStatus é a situação de uma missão: a gravada, ou a que sai das datas e do progresso.
func missionStatus(m store.ShieldMission, now time.Time) string {
	switch {
	case m.State == "rascunho" || m.State == "cancelada":
		return m.State
	case m.CompletedAt != nil:
		return "concluida"
	case now.Before(m.StartsAt):
		return "agendada"
	case !now.Before(m.EndsAt):
		return "encerrada"
	}
	return "ativa"
}

// shieldCollectiveProgress: a porcentagem das máquinas do escopo que cumprem a meta (o
// denominador inclui as sem dado: nada some da conta).
func shieldCollectiveProgress(m store.ShieldMission, pol *shield.Policy, in shieldInputs, scores map[string]store.ShieldScore) (done, total int) {
	for _, mc := range in.machines {
		if !pol.InScope(mc.Ring) || (m.Ring != nil && *m.Ring != mc.Ring) {
			continue
		}
		total++
		s, ok := scores[mc.ID]
		if !ok {
			continue
		}
		if m.Goal.Criterion == "" {
			if s.Result.Sealed {
				done++
			}
			continue
		}
		for _, c := range s.Result.Criteria {
			if c.Criterion == m.Goal.Criterion && c.Status == shield.Conforme {
				done++
			}
		}
	}
	return done, total
}

// shieldCollective confere as missões coletivas ativas: chegou à meta, conclui.
func (a *api) shieldCollective(ctx context.Context, db shieldStore, pol *shield.Policy, in shieldInputs, now time.Time) error {
	ms, err := db.ShieldMissions(ctx, 200)
	if err != nil {
		return err
	}
	var scores map[string]store.ShieldScore
	for _, m := range ms {
		if m.Scope != "coletiva" || missionStatus(m, now) != "ativa" {
			continue
		}
		if scores == nil {
			if scores, err = db.ShieldScores(ctx); err != nil {
				return err
			}
		}
		done, total := shieldCollectiveProgress(m, pol, in, scores)
		if done != m.Done || total != m.Total {
			if err := db.SetShieldMissionProgress(ctx, m.ID, done, total); err != nil {
				return err
			}
		}
		if total > 0 && done*100 >= m.Goal.Percent*total {
			if ok, err := db.CompleteShieldMission(ctx, m.ID, now); err != nil {
				return err
			} else if ok {
				a.logger.Info("Shield: missão coletiva cumprida", "missao", m.ID, "titulo", m.Title, "maquinas", done, "de", total)
			}
		}
	}
	return nil
}

// shieldCloseDays fecha os dias que terminaram (até shieldCatchUp para trás, na partida).
func (a *api) shieldCloseDays(ctx context.Context, db shieldStore, pol *shield.Policy, today time.Time) error {
	a.shield.mu.Lock()
	closed := a.shield.closed
	a.shield.mu.Unlock()
	yesterday := today.AddDate(0, 0, -1)
	if closed == yesterday.Format(time.DateOnly) {
		return nil
	}
	from := today.AddDate(0, 0, -shieldCatchUp)
	if c, err := time.Parse(time.DateOnly, closed); err == nil && c.After(from) {
		from = c.AddDate(0, 0, 1)
	}
	for d := from; d.Before(today); d = d.AddDate(0, 0, 1) {
		if err := a.shieldCloseDay(ctx, db, pol, d); err != nil {
			return fmt.Errorf("fechar %s: %w", d.Format(time.DateOnly), err)
		}
	}
	if _, err := db.PruneShield(ctx, today.Add(-shieldRetention)); err != nil {
		return err
	}
	a.shield.mu.Lock()
	a.shield.closed = yesterday.Format(time.DateOnly)
	a.shield.mu.Unlock()
	return nil
}

// shieldCloseDay fecha um dia: a sequência e o XP de cada pessoa que usou máquina nele, as
// missões individuais, as coletivas cumpridas e as conquistas.
func (a *api) shieldCloseDay(ctx context.Context, db shieldStore, pol *shield.Policy, day time.Time) error {
	inputs, err := db.ShieldDayInputs(ctx, day)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(inputs))
	for id := range inputs {
		ids = append(ids, id)
	}
	players, err := db.ShieldPlayersByID(ctx, ids)
	if err != nil {
		return err
	}
	touched := map[int64]bool{}
	var commits []store.ShieldCommit
	for id, machines := range inputs {
		pl, ok := players[id]
		if !ok {
			continue
		}
		st := pl.Streak
		v := shield.Verdict(pol, day, machines)
		if !st.Step(day, v) {
			continue // dia já fechado (por outro servidor, ou antes de reiniciar)
		}
		commits = append(commits, store.ShieldCommit{PlayerID: id, Day: day, Streak: &st, Events: shield.DayXP(pol, day, v, st), MaxPerDay: pol.XP.MaxPerDay})
		touched[id] = true
	}
	if err := shieldCommitAll(ctx, db, commits); err != nil {
		return err
	}
	// Missões: as individuais com o dia dentro, e as coletivas cumpridas.
	ms, err := db.ShieldMissions(ctx, 200)
	if err != nil {
		return err
	}
	end := day.Add(24*time.Hour - time.Nanosecond)
	for _, m := range ms {
		if m.State != "publicada" || m.XP < 0 {
			continue
		}
		ev := shield.XPEvent{Kind: shield.XPMission, Ref: fmt.Sprint(m.ID), Points: m.XP, Reason: "missão: " + m.Title}
		switch {
		case m.Scope == "individual" && !day.Before(dateOf(m.StartsAt, time.UTC)) && !day.After(dateOf(m.EndsAt, time.UTC)):
			counts, err := db.ShieldProtectedDaysBetween(ctx, m.StartsAt, end)
			if err != nil {
				return err
			}
			inScope, err := db.ShieldPlayersInRings(ctx, m.Ring, m.StartsAt, end)
			if err != nil {
				return err
			}
			for _, pid := range inScope {
				if counts[pid] >= m.Goal.Days {
					if _, err := db.CommitShield(ctx, store.ShieldCommit{PlayerID: pid, Day: day, Events: []shield.XPEvent{ev}}); err != nil {
						return err
					}
					touched[pid] = true
				}
			}
		case m.Scope == "coletiva" && m.CompletedAt != nil && !m.CompletedAt.Before(m.StartsAt) && day.Sub(dateOf(*m.CompletedAt, time.UTC)) < 7*24*time.Hour:
			inScope, err := db.ShieldPlayersInRings(ctx, m.Ring, m.StartsAt, *m.CompletedAt)
			if err != nil {
				return err
			}
			for _, pid := range inScope {
				if _, err := db.CommitShield(ctx, store.ShieldCommit{PlayerID: pid, Day: day, Events: []shield.XPEvent{ev}}); err != nil {
					return err
				}
				touched[pid] = true
			}
		}
	}
	if len(touched) == 0 {
		return nil
	}
	tids := make([]int64, 0, len(touched))
	for id := range touched {
		tids = append(tids, id)
	}
	counters, have, err := db.ShieldCounters(ctx, tids)
	if err != nil {
		return err
	}
	commits = commits[:0]
	for _, id := range tids {
		if earned := shield.Earned(pol, counters[id], have[id]); len(earned) > 0 {
			commits = append(commits, store.ShieldCommit{PlayerID: id, Day: day, Badges: earned})
		}
	}
	return shieldCommitAll(ctx, db, commits)
}

// shieldCommitWorkers: quantas transações de fechamento ao mesmo tempo (cada uma é de uma
// pessoa só, com a linha dela trancada: não disputam entre si).
const shieldCommitWorkers = 6

// shieldCommitAll grava os fechamentos em paralelo, limitado; o primeiro erro para o resto.
func shieldCommitAll(ctx context.Context, db shieldStore, list []store.ShieldCommit) error {
	if len(list) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan store.ShieldCommit)
	errs := make(chan error, shieldCommitWorkers)
	var wg sync.WaitGroup
	for range min(shieldCommitWorkers, len(list)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if _, err := db.CommitShield(ctx, c); err != nil {
					errs <- err
					cancel()
					return
				}
			}
		}()
	}
feed:
	for _, c := range list {
		select {
		case work <- c:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return ctx.Err()
	}
}
