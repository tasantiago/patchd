package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
)

// PatchD Shield (migração 0082, docs/shield.md). Só no PostgreSQL: sem ele, o módulo não liga.

// LockShield é a chave da trava do avaliador do Shield: um servidor por vez recalcula e fecha
// o dia (os outros pulam a vez).
const LockShield int64 = 0x7061_7463_6864_0003

// ShieldPolicyRow é uma versão da política.
type ShieldPolicyRow struct {
	Version   int
	Body      []byte
	Note      string
	CreatedBy string
	CreatedAt time.Time
}

// ShieldPolicy devolve a versão mais nova (ok falso: nenhuma gravada, vale a padrão).
func (p *Postgres) ShieldPolicy(ctx context.Context) (ShieldPolicyRow, bool, error) {
	var r ShieldPolicyRow
	err := p.pool.QueryRow(ctx, `SELECT version, body, note, created_by, created_at FROM shield_policies ORDER BY version DESC LIMIT 1`).
		Scan(&r.Version, &r.Body, &r.Note, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return r, err == nil, err
}

// ShieldPolicyHistory: as últimas versões, da mais nova para a mais velha.
func (p *Postgres) ShieldPolicyHistory(ctx context.Context, limit int) ([]ShieldPolicyRow, error) {
	rows, err := p.pool.Query(ctx, `SELECT version, body, note, created_by, created_at FROM shield_policies ORDER BY version DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldPolicyRow, error) {
		var x ShieldPolicyRow
		err := r.Scan(&x.Version, &x.Body, &x.Note, &x.CreatedBy, &x.CreatedAt)
		x.CreatedAt = x.CreatedAt.UTC()
		return x, err
	})
}

// SaveShieldPolicy grava uma versão nova (a política já validada por quem chama).
func (p *Postgres) SaveShieldPolicy(ctx context.Context, pol shield.Policy, note, by string) (int, error) {
	if err := pol.Validate(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(by) == "" || len(note) > 200 {
		return 0, errors.New("política do Shield: autor obrigatório e nota de até 200 caracteres")
	}
	b, err := json.Marshal(pol)
	if err != nil {
		return 0, err
	}
	var v int
	err = p.pool.QueryRow(ctx, `INSERT INTO shield_policies (body, note, created_by) VALUES ($1, $2, $3) RETURNING version`, b, note, by).Scan(&v)
	return v, err
}

// ShieldScore é a pontuação guardada de uma máquina.
type ShieldScore struct {
	MachineID     string
	PolicyVersion int
	Result        shield.Result
	EvaluatedAt   time.Time
	ChangedAt     time.Time
}

// ShieldScores: as pontuações guardadas das máquinas ativas.
func (p *Postgres) ShieldScores(ctx context.Context) (map[string]ShieldScore, error) {
	rows, err := p.pool.Query(ctx, `SELECT s.machine_id, s.policy_version, s.details, s.evaluated_at, s.changed_at
		FROM shield_scores s JOIN machines m ON m.id = s.machine_id WHERE m.retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ShieldScore{}
	for rows.Next() {
		var s ShieldScore
		var det []byte
		if err := rows.Scan(&s.MachineID, &s.PolicyVersion, &det, &s.EvaluatedAt, &s.ChangedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(det, &s.Result); err != nil {
			return nil, fmt.Errorf("pontuação de %s: %w", s.MachineID, err)
		}
		s.EvaluatedAt, s.ChangedAt = s.EvaluatedAt.UTC(), s.ChangedAt.UTC()
		out[s.MachineID] = s
	}
	return out, rows.Err()
}

// ShieldScoreOf: a pontuação de uma máquina.
func (p *Postgres) ShieldScoreOf(ctx context.Context, id string) (ShieldScore, bool, error) {
	s := ShieldScore{MachineID: id}
	var det []byte
	err := p.pool.QueryRow(ctx, `SELECT policy_version, details, evaluated_at, changed_at FROM shield_scores WHERE machine_id = $1`, id).
		Scan(&s.PolicyVersion, &det, &s.EvaluatedAt, &s.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s.EvaluatedAt, s.ChangedAt = s.EvaluatedAt.UTC(), s.ChangedAt.UTC()
	return s, true, json.Unmarshal(det, &s.Result)
}

// SaveShieldScores grava as pontuações em lote (uma ida ao banco por lote).
func (p *Postgres) SaveShieldScores(ctx context.Context, list []ShieldScore) error {
	const chunk = 500
	for len(list) > 0 {
		n := min(chunk, len(list))
		b := &pgx.Batch{}
		for _, s := range list[:n] {
			det, err := json.Marshal(s.Result)
			if err != nil {
				return err
			}
			b.Queue(`INSERT INTO shield_scores (machine_id, policy_version, score, sealed, overall, user_fault, blame, details, evaluated_at, changed_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				ON CONFLICT (machine_id) DO UPDATE SET policy_version=excluded.policy_version, score=excluded.score, sealed=excluded.sealed,
				overall=excluded.overall, user_fault=excluded.user_fault, blame=excluded.blame, details=excluded.details,
				evaluated_at=excluded.evaluated_at, changed_at=excluded.changed_at`,
				s.MachineID, s.PolicyVersion, s.Result.Score, s.Result.Sealed, string(s.Result.Overall), s.Result.UserFault,
				string(s.Result.Blame), det, s.EvaluatedAt, s.ChangedAt)
		}
		if err := p.pool.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		list = list[n:]
	}
	return nil
}

// DeleteShieldScores tira as pontuações das máquinas que saíram do Shield (fora do piloto).
func (p *Postgres) DeleteShieldScores(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx, `DELETE FROM shield_scores WHERE machine_id = ANY($1)`, ids)
	return err
}

// ShieldDaily é o retrato do dia de uma máquina.
type ShieldDaily struct {
	MachineID     string
	Day           time.Time // só a data
	Score         *float64
	Sealed        bool
	UserFault     bool
	NoData        bool
	Ring          int
	PolicyVersion int
}

// SaveShieldDaily grava (ou troca) o retrato do dia: o último do dia é o que vale.
func (p *Postgres) SaveShieldDaily(ctx context.Context, list []ShieldDaily) error {
	const chunk = 500
	for len(list) > 0 {
		n := min(chunk, len(list))
		b := &pgx.Batch{}
		for _, d := range list[:n] {
			b.Queue(`INSERT INTO shield_daily (machine_id, day, score, sealed, user_fault, no_data, ring, policy_version)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (machine_id, day) DO UPDATE SET score=excluded.score, sealed=excluded.sealed, user_fault=excluded.user_fault,
				no_data=excluded.no_data, ring=excluded.ring, policy_version=excluded.policy_version`,
				d.MachineID, d.Day.Format(time.DateOnly), d.Score, d.Sealed, d.UserFault, d.NoData, d.Ring, d.PolicyVersion)
		}
		if err := p.pool.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		list = list[n:]
	}
	return nil
}

// ShieldTrend: a evolução do parque do Shield, dia a dia (máquinas, no selo, média).
type ShieldTrend struct {
	Day      time.Time
	Machines int
	Sealed   int
	Average  float64
}

// ShieldTrends: os últimos days dias.
func (p *Postgres) ShieldTrends(ctx context.Context, since time.Time) ([]ShieldTrend, error) {
	rows, err := p.pool.Query(ctx, `SELECT day, count(*), count(*) FILTER (WHERE sealed), coalesce(avg(coalesce(score, 0)), 0)::float8
		FROM shield_daily WHERE day >= $1 GROUP BY day ORDER BY day`, since.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldTrend, error) {
		var t ShieldTrend
		err := r.Scan(&t.Day, &t.Machines, &t.Sealed, &t.Average)
		return t, err
	})
}

// ShieldMachine é o que o avaliador precisa de cada máquina ativa além da compliance: o
// grupo, a versão do agente e quem está conectado.
type ShieldMachine struct {
	ID           string
	Hostname     string
	Ring         int
	AgentVersion string
	Users        []protocol.LoggedUser
}

// ShieldMachines: as máquinas ativas, numa consulta só.
func (p *Postgres) ShieldMachines(ctx context.Context) ([]ShieldMachine, error) {
	rows, err := p.pool.Query(ctx, `SELECT m.id, coalesce(i.hostname, ''), m.ring, m.agent_version,
		CASE WHEN jsonb_typeof(m.state->'users') = 'array' THEN m.state->'users' ELSE '[]'::jsonb END
		FROM machines m LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE m.retired_at IS NULL ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldMachine, error) {
		var m ShieldMachine
		var users []byte
		if err := r.Scan(&m.ID, &m.Hostname, &m.Ring, &m.AgentVersion, &users); err != nil {
			return m, err
		}
		_ = json.Unmarshal(users, &m.Users) // estado fora do formato: ninguém conectado
		return m, nil
	})
}

// StandardizationSummary é o resumo da padronização de uma máquina (todas as contas).
type StandardizationSummary struct {
	Applies         bool // há plano com algo a fazer
	Known           bool // o resultado dos atalhos já chegou (ou não há atalho)
	Failed, Waiting int
	Conflicts       int
}

// StandardizationSummaries resume a padronização de todas as máquinas ativas.
func (p *Postgres) StandardizationSummaries(ctx context.Context) (map[string]StandardizationSummary, error) {
	rows, err := p.pool.Query(ctx, `SELECT s.machine_id, s.plan, s.shortcut_result FROM standardization_states s
		JOIN machines m ON m.id = s.machine_id WHERE m.retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StandardizationSummary{}
	for rows.Next() {
		var id string
		var planB, resB []byte
		if err := rows.Scan(&id, &planB, &resB); err != nil {
			return nil, err
		}
		var plan protocol.StandardizationPlan
		if json.Unmarshal(planB, &plan) != nil {
			continue
		}
		s := out[id]
		if len(plan.Apps) == 0 && len(plan.Shortcuts) == 0 && len(plan.Policies) == 0 {
			if !s.Applies {
				out[id] = s
			}
			continue
		}
		if !s.Applies {
			s.Known = true
		}
		s.Applies = true
		for _, a := range plan.Apps {
			switch a.State {
			case "falhou":
				s.Failed++
			case "em_dia":
			default:
				s.Waiting++
			}
		}
		if len(plan.Shortcuts) > 0 {
			if resB == nil {
				s.Known = false
			} else {
				var res protocol.StandardizationResult
				if json.Unmarshal(resB, &res) == nil {
					for _, it := range res.Items {
						if it.State == "conflito" {
							s.Conflicts++
						}
					}
				}
			}
		}
		out[id] = s
	}
	return out, rows.Err()
}

// ShieldPlayer é uma pessoa.
type ShieldPlayer struct {
	ID          int64
	Account     string
	SID         string
	DisplayName string
	XP          int64
	Streak      shield.Streak
	SeenAt      time.Time
}

const playerCols = `id, account, coalesce(sid, ''), display_name, xp, streak, seen_at`

func scanPlayer(r pgx.Row) (ShieldPlayer, error) {
	var pl ShieldPlayer
	var st []byte
	if err := r.Scan(&pl.ID, &pl.Account, &pl.SID, &pl.DisplayName, &pl.XP, &st, &pl.SeenAt); err != nil {
		return pl, err
	}
	_ = json.Unmarshal(st, &pl.Streak)
	pl.SeenAt = pl.SeenAt.UTC()
	return pl, nil
}

// NormalizeAccount é a chave da pessoa: a conta em minúsculas, sem espaços nas pontas.
func NormalizeAccount(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

// ShieldPlayerFor acha (ou cria) a pessoa pela identidade que o agente leu do token. O SID
// manda: a conta renomeada continua a mesma pessoa. Sem SID, a conta.
func (p *Postgres) ShieldPlayerFor(ctx context.Context, account, sid, display string) (ShieldPlayer, error) {
	acc := NormalizeAccount(account)
	if acc == "" || len(acc) > 200 || len(sid) > 200 || len(display) > 200 {
		return ShieldPlayer{}, errors.New("identidade fora do formato")
	}
	var out ShieldPlayer
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if sid != "" {
			pl, err := scanPlayer(tx.QueryRow(ctx, `SELECT `+playerCols+` FROM shield_players WHERE sid = $1 FOR UPDATE`, sid))
			if err == nil {
				if pl.Account != acc {
					// Renomeada: se outra linha tem a conta nova sem SID, ela é a mesma pessoa vista
					// pelo check-in; a do SID fica, e a outra solta a conta.
					if _, err := tx.Exec(ctx, `UPDATE shield_players SET account = account || '#' || id WHERE account = $1 AND id <> $2 AND sid IS NULL`, acc, pl.ID); err != nil {
						return err
					}
				}
				out, err = scanPlayer(tx.QueryRow(ctx, `UPDATE shield_players SET account = $2, display_name = CASE WHEN $3 <> '' THEN $3 ELSE display_name END, seen_at = now()
					WHERE id = $1 RETURNING `+playerCols, pl.ID, acc, display))
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var sidArg any
		if sid != "" {
			sidArg = sid
		}
		var err error
		out, err = scanPlayer(tx.QueryRow(ctx, `INSERT INTO shield_players (account, sid, display_name) VALUES ($1, $2, $3)
			ON CONFLICT (account) DO UPDATE SET sid = coalesce(shield_players.sid, excluded.sid),
			display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE shield_players.display_name END, seen_at = now()
			RETURNING `+playerCols, acc, sidArg, display))
		return err
	})
	return out, err
}

// ShieldTouchAccounts garante uma pessoa para cada conta vista nas sessões (sem SID) e devolve
// conta → id, numa ida ao banco.
func (p *Postgres) ShieldTouchAccounts(ctx context.Context, names map[string]string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(names) == 0 {
		return out, nil
	}
	accs := make([]string, 0, len(names))
	disp := make([]string, 0, len(names))
	for a, d := range names {
		accs, disp = append(accs, NormalizeAccount(a)), append(disp, d)
	}
	rows, err := p.pool.Query(ctx, `INSERT INTO shield_players (account, display_name)
		SELECT a, d FROM unnest($1::text[], $2::text[]) AS t(a, d)
		ON CONFLICT (account) DO UPDATE SET display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE shield_players.display_name END,
		seen_at = now()
		RETURNING id, account`, accs, disp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, err
		}
		out[a] = id
	}
	return out, rows.Err()
}

// ShieldPresence é uma pessoa numa máquina num dia.
type ShieldPresence struct {
	PlayerID  int64
	MachineID string
	Day       time.Time
}

// AddShieldPresence grava as presenças (as repetidas são ignoradas).
func (p *Postgres) AddShieldPresence(ctx context.Context, list []ShieldPresence) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	ms := make([]string, len(list))
	days := make([]string, len(list))
	for i, x := range list {
		ids[i], ms[i], days[i] = x.PlayerID, x.MachineID, x.Day.Format(time.DateOnly)
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO shield_presence (player_id, machine_id, day)
		SELECT i, m, d::date FROM unnest($1::bigint[], $2::text[], $3::text[]) AS t(i, m, d)
		WHERE EXISTS (SELECT 1 FROM machines WHERE id = m)
		ON CONFLICT DO NOTHING`, ids, ms, days)
	return err
}

// ShieldPresentOn: quem usou cada máquina no dia (máquina → pessoas).
func (p *Postgres) ShieldPresentOn(ctx context.Context, day time.Time) (map[string][]int64, error) {
	rows, err := p.pool.Query(ctx, `SELECT machine_id, player_id FROM shield_presence WHERE day = $1`, day.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var m string
		var id int64
		if err := rows.Scan(&m, &id); err != nil {
			return nil, err
		}
		out[m] = append(out[m], id)
	}
	return out, rows.Err()
}

// ShieldDayInputs: para cada pessoa que usou máquina no dia, o fim do dia de cada uma delas
// (só as que estavam no Shield: com retrato).
func (p *Postgres) ShieldDayInputs(ctx context.Context, day time.Time) (map[int64][]shield.MachineDay, error) {
	rows, err := p.pool.Query(ctx, `SELECT pr.player_id, d.machine_id, d.sealed, d.user_fault, d.no_data
		FROM shield_presence pr JOIN shield_daily d ON d.machine_id = pr.machine_id AND d.day = pr.day
		WHERE pr.day = $1 ORDER BY pr.player_id, d.machine_id`, day.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]shield.MachineDay{}
	for rows.Next() {
		var id int64
		var m shield.MachineDay
		if err := rows.Scan(&id, &m.MachineID, &m.Sealed, &m.UserFault, &m.NoData); err != nil {
			return nil, err
		}
		out[id] = append(out[id], m)
	}
	return out, rows.Err()
}

// ShieldPlayersByID lê as pessoas.
func (p *Postgres) ShieldPlayersByID(ctx context.Context, ids []int64) (map[int64]ShieldPlayer, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+playerCols+` FROM shield_players WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ShieldPlayer{}
	for rows.Next() {
		pl, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out[pl.ID] = pl
	}
	return out, rows.Err()
}

// ShieldPlayerByAccount acha a pessoa pela conta (ok falso: não existe).
func (p *Postgres) ShieldPlayerByAccount(ctx context.Context, account string) (ShieldPlayer, bool, error) {
	pl, err := scanPlayer(p.pool.QueryRow(ctx, `SELECT `+playerCols+` FROM shield_players WHERE account = $1`, NormalizeAccount(account)))
	if errors.Is(err, pgx.ErrNoRows) {
		return pl, false, nil
	}
	return pl, err == nil, err
}

// ShieldCommit é o que o fechamento grava de uma pessoa, numa transação: a sequência (só se o
// dia é posterior ao último fechado), os eventos de XP (os repetidos não contam, com o teto do
// dia) e as conquistas novas (com o XP delas).
type ShieldCommit struct {
	PlayerID  int64
	Day       time.Time
	Streak    *shield.Streak // nil: não mexe na sequência
	Events    []shield.XPEvent
	Badges    []shield.Achievement
	MaxPerDay int // o teto do dia (0: sem teto)
}

// ShieldCommitted é o que entrou de fato.
type ShieldCommitted struct {
	Points int
	Events []shield.XPEvent
	Badges []string
}

// CommitShield grava o fechamento de uma pessoa.
func (p *Postgres) CommitShield(ctx context.Context, c ShieldCommit) (ShieldCommitted, error) {
	var out ShieldCommitted
	day := c.Day.Format(time.DateOnly)
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		out = ShieldCommitted{}
		// A linha da pessoa trancada: dois fechamentos ao mesmo tempo não somam duas vezes.
		var st []byte
		if err := tx.QueryRow(ctx, `SELECT streak FROM shield_players WHERE id = $1 FOR UPDATE`, c.PlayerID).Scan(&st); err != nil {
			return err
		}
		if c.Streak != nil {
			var cur shield.Streak
			_ = json.Unmarshal(st, &cur)
			if cur.LastDay == "" || cur.LastDay < c.Streak.LastDay {
				b, _ := json.Marshal(c.Streak)
				if _, err := tx.Exec(ctx, `UPDATE shield_players SET streak = $2 WHERE id = $1`, c.PlayerID, b); err != nil {
					return err
				}
			}
		}
		events := c.Events
		for _, a := range c.Badges {
			tag, err := tx.Exec(ctx, `INSERT INTO shield_badges (player_id, code) VALUES ($1, $2) ON CONFLICT DO NOTHING`, c.PlayerID, a.Code)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				out.Badges = append(out.Badges, a.Code)
				if a.XP > 0 {
					events = append(events, shield.XPEvent{Kind: shield.XPAchievement, Ref: a.Code, Points: a.XP, Reason: "conquista: " + a.Name})
				}
			}
		}
		if c.MaxPerDay > 0 && len(events) > 0 {
			var already int
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(points), 0) FROM shield_xp WHERE player_id = $1 AND day = $2 AND kind <> 'ajuste'`,
				c.PlayerID, day).Scan(&already); err != nil {
				return err
			}
			events = shield.CapDay(&shield.Policy{XP: shield.XPRules{MaxPerDay: c.MaxPerDay}}, already, events)
		}
		for _, e := range events {
			tag, err := tx.Exec(ctx, `INSERT INTO shield_xp (player_id, kind, ref, points, day, reason) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (player_id, kind, ref) DO NOTHING`, c.PlayerID, e.Kind, e.Ref, e.Points, day, e.Reason)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				out.Points += e.Points
				out.Events = append(out.Events, e)
			}
		}
		if out.Points != 0 {
			_, err := tx.Exec(ctx, `UPDATE shield_players SET xp = xp + $2 WHERE id = $1`, c.PlayerID, out.Points)
			return err
		}
		return nil
	})
	return out, err
}

// ErrShieldNotFound: a máquina (ou a missão em rascunho) não existe.
var ErrShieldNotFound = errors.New("não encontrado")

// ErrShieldAdjust: ajuste fora das regras.
var ErrShieldAdjust = errors.New("ajuste de XP: motivo obrigatório (até 200 caracteres) e pontos de -10.000 a 10.000, diferentes de zero")

// AdjustShieldXP é a compensação do administrador: um evento "ajuste" com motivo e autor,
// chave única pelo ref que o painel gera (o reenvio do formulário não soma duas vezes).
func (p *Postgres) AdjustShieldXP(ctx context.Context, playerID int64, points int, reason, actor, ref string, now time.Time) (bool, error) {
	reason = strings.TrimSpace(reason)
	if points == 0 || points < -10000 || points > 10000 || reason == "" || len([]rune(reason)) > 200 || strings.TrimSpace(actor) == "" || ref == "" || len(ref) > 64 {
		return false, ErrShieldAdjust
	}
	done := false
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO shield_xp (player_id, kind, ref, points, day, reason, actor) VALUES ($1,'ajuste',$2,$3,$4,$5,$6)
			ON CONFLICT (player_id, kind, ref) DO NOTHING`, playerID, ref, points, now.Format(time.DateOnly), reason, actor)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		done = true
		_, err = tx.Exec(ctx, `UPDATE shield_players SET xp = greatest(xp + $2, 0) WHERE id = $1`, playerID, points)
		return err
	})
	return done, err
}

// ShieldXPEntry é um evento de XP, para a pessoa e para a auditoria.
type ShieldXPEntry struct {
	Kind, Ref, Reason, Actor string
	Points                   int
	Day                      time.Time
	CreatedAt                time.Time
}

// ShieldXPOf: os últimos eventos de uma pessoa.
func (p *Postgres) ShieldXPOf(ctx context.Context, playerID int64, limit int) ([]ShieldXPEntry, error) {
	rows, err := p.pool.Query(ctx, `SELECT kind, ref, reason, actor, points, day, created_at FROM shield_xp WHERE player_id = $1 ORDER BY id DESC LIMIT $2`, playerID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldXPEntry, error) {
		var e ShieldXPEntry
		err := r.Scan(&e.Kind, &e.Ref, &e.Reason, &e.Actor, &e.Points, &e.Day, &e.CreatedAt)
		e.CreatedAt = e.CreatedAt.UTC()
		return e, err
	})
}

// ShieldCounters: as medidas das conquistas de cada pessoa (dos eventos e da sequência).
func (p *Postgres) ShieldCounters(ctx context.Context, ids []int64) (map[int64]shield.Counters, map[int64]map[string]bool, error) {
	out := map[int64]shield.Counters{}
	have := map[int64]map[string]bool{}
	pls, err := p.ShieldPlayersByID(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for id, pl := range pls {
		out[id] = shield.Counters{
			shield.MetricProtectedDays: pl.Streak.Protected, shield.MetricSealedDays: pl.Streak.Protected,
			shield.MetricStreak: pl.Streak.Current, shield.MetricBestStreak: pl.Streak.Best,
			shield.MetricComebacks: pl.Streak.Comebacks, shield.MetricXP: int(pl.XP),
		}
		have[id] = map[string]bool{}
	}
	rows, err := p.pool.Query(ctx, `SELECT player_id, kind, count(*) FROM shield_xp WHERE player_id = ANY($1) AND kind IN ('reinicio','missao','atualizacao') GROUP BY player_id, kind`, ids)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		var kind string
		var n int
		if err := rows.Scan(&id, &kind, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if c, ok := out[id]; ok {
			switch kind {
			case shield.XPReboot:
				c[shield.MetricReboots] = n
			case shield.XPMission:
				c[shield.MetricMissions] = n
			case shield.XPUpdate:
				c[shield.MetricUpdates] = n
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = p.pool.Query(ctx, `SELECT player_id, code FROM shield_badges WHERE player_id = ANY($1)`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, nil, err
		}
		if h, ok := have[id]; ok {
			h[code] = true
		}
	}
	return out, have, rows.Err()
}

// ShieldBadge é uma conquista ganha.
type ShieldBadge struct {
	Code     string
	EarnedAt time.Time
}

// ShieldBadgesOf: as conquistas de uma pessoa.
func (p *Postgres) ShieldBadgesOf(ctx context.Context, playerID int64) ([]ShieldBadge, error) {
	rows, err := p.pool.Query(ctx, `SELECT code, earned_at FROM shield_badges WHERE player_id = $1 ORDER BY earned_at`, playerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldBadge, error) {
		var b ShieldBadge
		err := r.Scan(&b.Code, &b.EarnedAt)
		b.EarnedAt = b.EarnedAt.UTC()
		return b, err
	})
}

// ShieldMachinesOf: as máquinas que a pessoa usou desde since (as mais recentes primeiro).
func (p *Postgres) ShieldMachinesOf(ctx context.Context, playerID int64, since time.Time) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT machine_id FROM shield_presence WHERE player_id = $1 AND day >= $2 GROUP BY machine_id ORDER BY max(day) DESC LIMIT 20`,
		playerID, since.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ShieldLeader é uma linha do ranking individual (só nome e números; nada da máquina).
type ShieldLeader struct {
	DisplayName string
	Account     string
	XP          int64
	Protected   int
	Streak      int
}

// ShieldLeaders: as pessoas com mais XP, vistas desde since.
func (p *Postgres) ShieldLeaders(ctx context.Context, since time.Time, limit int) ([]ShieldLeader, error) {
	rows, err := p.pool.Query(ctx, `SELECT display_name, account, xp, coalesce((streak->>'protected')::int, 0), coalesce((streak->>'current')::int, 0)
		FROM shield_players WHERE seen_at >= $1 AND xp > 0 ORDER BY xp DESC, id LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldLeader, error) {
		var l ShieldLeader
		err := r.Scan(&l.DisplayName, &l.Account, &l.XP, &l.Protected, &l.Streak)
		return l, err
	})
}

// ShieldPlayerList: as pessoas para o painel (busca na conta e no nome).
func (p *Postgres) ShieldPlayerList(ctx context.Context, q string, limit, offset int) ([]ShieldPlayer, int, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(q)) + "%"
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM shield_players WHERE account LIKE $1 OR lower(display_name) LIKE $1`, like).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, `SELECT `+playerCols+` FROM shield_players WHERE account LIKE $1 OR lower(display_name) LIKE $1
		ORDER BY xp DESC, id LIMIT $2 OFFSET $3`, like, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ShieldPlayer
	for rows.Next() {
		pl, err := scanPlayer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, pl)
	}
	return out, total, rows.Err()
}

// ShieldException é uma exceção aprovada.
type ShieldException struct {
	ID        int64
	MachineID string
	Hostname  string
	Criterion shield.Criterion
	Reason    string
	ExpiresAt time.Time
	CreatedBy string
	CreatedAt time.Time
	RevokedAt *time.Time
	RevokedBy string
}

// ErrShieldException: exceção fora das regras.
var ErrShieldException = errors.New("exceção: critério conhecido, motivo de 1 a 300 caracteres e validade de até 365 dias")

// AddShieldException grava uma exceção.
func (p *Postgres) AddShieldException(ctx context.Context, e ShieldException, now time.Time) (int64, error) {
	e.Reason = strings.TrimSpace(e.Reason)
	if !e.Criterion.Valid() || e.Reason == "" || len([]rune(e.Reason)) > 300 || !e.ExpiresAt.After(now) ||
		e.ExpiresAt.After(now.Add(366*24*time.Hour)) || strings.TrimSpace(e.CreatedBy) == "" {
		return 0, ErrShieldException
	}
	var id int64
	err := p.pool.QueryRow(ctx, `INSERT INTO shield_exceptions (machine_id, criterion, reason, expires_at, created_by)
		SELECT id, $2, $3, $4, $5 FROM machines WHERE id = $1 AND retired_at IS NULL RETURNING id`,
		e.MachineID, string(e.Criterion), e.Reason, e.ExpiresAt, e.CreatedBy).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrShieldNotFound
	}
	return id, err
}

// RevokeShieldException revoga (ok falso: não havia exceção vigente com esse id).
func (p *Postgres) RevokeShieldException(ctx context.Context, id int64, by string) (ShieldException, bool, error) {
	var e ShieldException
	err := p.pool.QueryRow(ctx, `UPDATE shield_exceptions SET revoked_at = now(), revoked_by = $2 WHERE id = $1 AND revoked_at IS NULL
		RETURNING id, machine_id, criterion, reason, expires_at`, id, by).Scan(&e.ID, &e.MachineID, &e.Criterion, &e.Reason, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// ShieldExceptions: as exceções (vigentes, ou todas com all), com o nome da máquina.
func (p *Postgres) ShieldExceptions(ctx context.Context, all bool, now time.Time) ([]ShieldException, error) {
	rows, err := p.pool.Query(ctx, `SELECT e.id, e.machine_id, coalesce(i.hostname, ''), e.criterion, e.reason, e.expires_at, e.created_by, e.created_at, e.revoked_at, e.revoked_by
		FROM shield_exceptions e JOIN machines m ON m.id = e.machine_id
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE $1 OR (e.revoked_at IS NULL AND e.expires_at > $2) ORDER BY e.id DESC LIMIT 1000`, all, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldException, error) {
		var e ShieldException
		err := r.Scan(&e.ID, &e.MachineID, &e.Hostname, &e.Criterion, &e.Reason, &e.ExpiresAt, &e.CreatedBy, &e.CreatedAt, &e.RevokedAt, &e.RevokedBy)
		e.ExpiresAt, e.CreatedAt, e.RevokedAt = e.ExpiresAt.UTC(), e.CreatedAt.UTC(), utc(e.RevokedAt)
		return e, err
	})
}

// ShieldMission é uma missão.
type ShieldMission struct {
	ID          int64
	Title       string
	Description string
	Scope       string // individual ou coletiva
	Goal        MissionGoal
	Ring        *int
	StartsAt    time.Time
	EndsAt      time.Time
	State       string // rascunho, publicada ou cancelada
	XP          int
	CompletedAt *time.Time
	// Done e Total: o progresso da coletiva (máquinas que cumprem / do escopo), do avaliador.
	Done, Total int
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MissionGoal é a meta. Individual: Days dias protegidos dentro da missão. Coletiva: Percent%
// das máquinas do escopo com o selo (Criterion vazio) ou com o critério conforme.
type MissionGoal struct {
	Days      int              `json:"days,omitempty"`
	Percent   int              `json:"percent,omitempty"`
	Criterion shield.Criterion `json:"criterion,omitempty"`
}

// ErrShieldMission: missão fora das regras.
var ErrShieldMission = errors.New("missão: título de 1 a 80 caracteres, descrição até 500, fim depois do início (até 180 dias), meta válida e XP de 0 a 10.000")

func (m ShieldMission) check() error {
	t := strings.TrimSpace(m.Title)
	ok := t != "" && len([]rune(t)) <= 80 && len([]rune(m.Description)) <= 500 && m.EndsAt.After(m.StartsAt) &&
		m.EndsAt.Sub(m.StartsAt) <= 181*24*time.Hour && m.XP >= 0 && m.XP <= 10000 && strings.TrimSpace(m.CreatedBy) != ""
	switch m.Scope {
	case "individual":
		ok = ok && m.Goal.Days >= 1 && m.Goal.Days <= 180 && m.Goal.Percent == 0 && m.Goal.Criterion == ""
	case "coletiva":
		ok = ok && m.Goal.Percent >= 1 && m.Goal.Percent <= 100 && m.Goal.Days == 0 && (m.Goal.Criterion == "" || m.Goal.Criterion.Valid())
	default:
		ok = false
	}
	switch m.State {
	case "rascunho", "publicada", "cancelada":
	default:
		ok = false
	}
	if !ok {
		return ErrShieldMission
	}
	return nil
}

const missionCols = `id, title, description, scope, goal, ring, starts_at, ends_at, state, xp, completed_at, progress_done, progress_total, created_by, created_at, updated_at`

func scanMission(r pgx.CollectableRow) (ShieldMission, error) {
	var m ShieldMission
	var goal []byte
	err := r.Scan(&m.ID, &m.Title, &m.Description, &m.Scope, &goal, &m.Ring, &m.StartsAt, &m.EndsAt, &m.State, &m.XP, &m.CompletedAt,
		&m.Done, &m.Total, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(goal, &m.Goal)
	}
	m.StartsAt, m.EndsAt, m.CreatedAt, m.UpdatedAt, m.CompletedAt = m.StartsAt.UTC(), m.EndsAt.UTC(), m.CreatedAt.UTC(), m.UpdatedAt.UTC(), utc(m.CompletedAt)
	return m, err
}

// ShieldMissions: as missões, das mais novas para as mais velhas.
func (p *Postgres) ShieldMissions(ctx context.Context, limit int) ([]ShieldMission, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+missionCols+` FROM shield_missions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMission)
}

// SaveShieldMission grava uma missão nova (ID 0) ou muda uma que ainda é rascunho.
func (p *Postgres) SaveShieldMission(ctx context.Context, m ShieldMission) (int64, error) {
	if err := m.check(); err != nil {
		return 0, err
	}
	goal, _ := json.Marshal(m.Goal)
	if m.ID == 0 {
		err := p.pool.QueryRow(ctx, `INSERT INTO shield_missions (title, description, scope, goal, ring, starts_at, ends_at, state, xp, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, strings.TrimSpace(m.Title), m.Description, m.Scope, goal, m.Ring,
			m.StartsAt, m.EndsAt, m.State, m.XP, m.CreatedBy).Scan(&m.ID)
		return m.ID, err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE shield_missions SET title=$2, description=$3, scope=$4, goal=$5, ring=$6, starts_at=$7, ends_at=$8, state=$9, xp=$10, updated_at=now()
		WHERE id = $1 AND state = 'rascunho'`, m.ID, strings.TrimSpace(m.Title), m.Description, m.Scope, goal, m.Ring, m.StartsAt, m.EndsAt, m.State, m.XP)
	if err == nil && tag.RowsAffected() == 0 {
		return 0, ErrShieldNotFound
	}
	return m.ID, err
}

// SetShieldMissionState publica ou cancela (publicada só vira cancelada; rascunho vira qualquer uma).
func (p *Postgres) SetShieldMissionState(ctx context.Context, id int64, state string) (bool, error) {
	if state != "publicada" && state != "cancelada" {
		return false, ErrShieldMission
	}
	tag, err := p.pool.Exec(ctx, `UPDATE shield_missions SET state = $2, updated_at = now()
		WHERE id = $1 AND (state = 'rascunho' OR (state = 'publicada' AND $2 = 'cancelada'))`, id, state)
	return err == nil && tag.RowsAffected() == 1, err
}

// CompleteShieldMission marca a missão coletiva como cumprida (uma vez).
func (p *Postgres) CompleteShieldMission(ctx context.Context, id int64, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE shield_missions SET completed_at = $2 WHERE id = $1 AND completed_at IS NULL AND state = 'publicada'`, id, at)
	return err == nil && tag.RowsAffected() == 1, err
}

// SetShieldMissionProgress grava o progresso da coletiva.
func (p *Postgres) SetShieldMissionProgress(ctx context.Context, id int64, done, total int) error {
	_, err := p.pool.Exec(ctx, `UPDATE shield_missions SET progress_done = $2, progress_total = $3 WHERE id = $1`, id, done, total)
	return err
}

// ShieldProtectedDaysOf: os dias protegidos de uma pessoa no intervalo.
func (p *Postgres) ShieldProtectedDaysOf(ctx context.Context, playerID int64, from, to time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM shield_xp WHERE player_id = $1 AND kind = 'dia_protegido' AND day >= $2 AND day <= $3`,
		playerID, from.Format(time.DateOnly), to.Format(time.DateOnly)).Scan(&n)
	return n, err
}

// ShieldProtectedDaysBetween: quantos dias protegidos cada pessoa teve no intervalo (pelos
// eventos de XP do dia protegido, que são um por dia).
func (p *Postgres) ShieldProtectedDaysBetween(ctx context.Context, from, to time.Time) (map[int64]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT player_id, count(*) FROM shield_xp WHERE kind = 'dia_protegido' AND day >= $1 AND day <= $2 GROUP BY player_id`,
		from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ShieldPlayersInRings: as pessoas que usaram máquinas dos grupos no intervalo (nil: todos).
func (p *Postgres) ShieldPlayersInRings(ctx context.Context, ring *int, from, to time.Time) ([]int64, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT pr.player_id FROM shield_presence pr JOIN machines m ON m.id = pr.machine_id
		WHERE pr.day >= $1 AND pr.day <= $2 AND ($3::int IS NULL OR m.ring = $3)`, from.Format(time.DateOnly), to.Format(time.DateOnly), ring)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// PruneShield apaga os retratos e as presenças mais velhos que before.
func (p *Postgres) PruneShield(ctx context.Context, before time.Time) (int64, error) {
	d := before.Format(time.DateOnly)
	a, err := p.pool.Exec(ctx, `DELETE FROM shield_daily WHERE day < $1`, d)
	if err != nil {
		return 0, err
	}
	b, err := p.pool.Exec(ctx, `DELETE FROM shield_presence WHERE day < $1`, d)
	if err != nil {
		return 0, err
	}
	return a.RowsAffected() + b.RowsAffected(), nil
}

// ShieldRingStat é um grupo de implantação no ranking coletivo: as máquinas ativas (com ou
// sem pontuação: as sem dado contam no denominador), as no selo e a média (sem dado vale 0).
type ShieldRingStat struct {
	Ring     int
	Machines int
	Scored   int
	Sealed   int
	Average  float64
}

// ShieldRingStats: os grupos, numa consulta agregada.
func (p *Postgres) ShieldRingStats(ctx context.Context) ([]ShieldRingStat, error) {
	rows, err := p.pool.Query(ctx, `SELECT m.ring, count(*), count(s.machine_id), count(*) FILTER (WHERE s.sealed),
		coalesce(avg(coalesce(s.score, 0)), 0)::float8
		FROM machines m LEFT JOIN shield_scores s ON s.machine_id = m.id
		WHERE m.retired_at IS NULL GROUP BY m.ring ORDER BY m.ring`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShieldRingStat, error) {
		var s ShieldRingStat
		err := r.Scan(&s.Ring, &s.Machines, &s.Scored, &s.Sealed, &s.Average)
		return s, err
	})
}
