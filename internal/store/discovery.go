package store

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Descoberta de rede (Aula 23.7): as faixas que o admin cadastra, os hosts vivos da última
// varredura e o histórico das varreduras. Os hosts são regravados por inteiro a cada
// varredura (uma foto da rede); as faixas e o histórico ficam.

// DiscoveryRange é uma faixa de busca cadastrada.
type DiscoveryRange struct {
	ID        int64
	Spec      string
	Note      string
	Enabled   bool
	CreatedAt time.Time
	CreatedBy string
}

// DiscoveryHost é um host vivo da última varredura. Match* preenchidos quando casou com uma
// máquina do inventário.
type DiscoveryHost struct {
	IP          string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Ports       string
	Hostname    string
	OSGuess     string
	MatchID     string // "" = sem agente
	MatchKind   string // "ip" | "nome" | ""
}

// DiscoveryRun é uma varredura registrada.
type DiscoveryRun struct {
	ID           int64
	StartedAt    time.Time
	FinishedAt   *time.Time
	Scanned      int
	Alive        int
	WithoutAgent int
	Partial      bool
	StartedBy    string
	Note         string
}

// ErrRangeExists: a faixa já está cadastrada (spec repetido).
var ErrRangeExists = errors.New("faixa já cadastrada")

// --- Postgres ----------------------------------------------------------------------------

func (p *Postgres) AddDiscoveryRange(ctx context.Context, spec, note, by string) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO discovery_ranges (spec, note, created_by) VALUES ($1, $2, $3)
		ON CONFLICT (spec) DO NOTHING RETURNING id`, spec, note, by).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrRangeExists
	}
	return id, err
}

func (p *Postgres) SetDiscoveryRangeEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE discovery_ranges SET enabled = $2 WHERE id = $1`, id, enabled)
	return tag.RowsAffected() > 0, err
}

func (p *Postgres) DeleteDiscoveryRange(ctx context.Context, id int64) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM discovery_ranges WHERE id = $1`, id)
	return tag.RowsAffected() > 0, err
}

func (p *Postgres) DiscoveryRanges(ctx context.Context) ([]DiscoveryRange, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, spec, note, enabled, created_at, created_by FROM discovery_ranges ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DiscoveryRange, error) {
		var d DiscoveryRange
		err := r.Scan(&d.ID, &d.Spec, &d.Note, &d.Enabled, &d.CreatedAt, &d.CreatedBy)
		d.CreatedAt = d.CreatedAt.UTC()
		return d, err
	})
}

// ReplaceDiscoveryHosts regrava a lista de hosts vivos e registra a varredura. first_seen_at
// é preservado de um host que já existia (o run informa seu começo como "agora" dos novos).
func (p *Postgres) ReplaceDiscoveryHosts(ctx context.Context, hosts []DiscoveryHost, run DiscoveryRun) (int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	prev := map[string]time.Time{}
	rows, err := tx.Query(ctx, `SELECT host(ip), first_seen_at FROM discovery_hosts`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var ip string
		var at time.Time
		if err := rows.Scan(&ip, &at); err != nil {
			rows.Close()
			return 0, err
		}
		prev[ip] = at
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM discovery_hosts`); err != nil {
		return 0, err
	}
	copyRows := make([][]any, 0, len(hosts))
	for _, h := range hosts {
		first := run.StartedAt
		if t, ok := prev[h.IP]; ok {
			first = t
		}
		var matchID any
		if h.MatchID != "" {
			matchID = h.MatchID
		}
		copyRows = append(copyRows, []any{h.IP, first, run.StartedAt, h.Ports, h.Hostname, h.OSGuess, matchID, h.MatchKind})
	}
	if len(copyRows) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"discovery_hosts"},
			[]string{"ip", "first_seen_at", "last_seen_at", "ports", "hostname", "os_guess", "match_id", "match_kind"},
			pgx.CopyFromRows(copyRows)); err != nil {
			return 0, err
		}
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO discovery_runs (started_at, finished_at, scanned, alive, without_agent, partial, started_by, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		run.StartedAt, run.FinishedAt, run.Scanned, run.Alive, run.WithoutAgent, run.Partial, run.StartedBy, run.Note).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM discovery_runs WHERE id NOT IN (SELECT id FROM discovery_runs ORDER BY id DESC LIMIT 50)`); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (p *Postgres) DiscoveryHosts(ctx context.Context) ([]DiscoveryHost, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT host(ip), first_seen_at, last_seen_at, ports, hostname, os_guess, coalesce(match_id, ''), match_kind
		FROM discovery_hosts ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DiscoveryHost, error) {
		var h DiscoveryHost
		err := r.Scan(&h.IP, &h.FirstSeenAt, &h.LastSeenAt, &h.Ports, &h.Hostname, &h.OSGuess, &h.MatchID, &h.MatchKind)
		h.FirstSeenAt, h.LastSeenAt = h.FirstSeenAt.UTC(), h.LastSeenAt.UTC()
		return h, err
	})
}

func (p *Postgres) DiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRun, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, started_at, finished_at, scanned, alive, without_agent, partial, started_by, note
		FROM discovery_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DiscoveryRun, error) {
		var d DiscoveryRun
		err := r.Scan(&d.ID, &d.StartedAt, &d.FinishedAt, &d.Scanned, &d.Alive, &d.WithoutAgent, &d.Partial, &d.StartedBy, &d.Note)
		d.StartedAt = d.StartedAt.UTC()
		d.FinishedAt = utc(d.FinishedAt)
		return d, err
	})
}

// --- Memória -----------------------------------------------------------------------------

func (m *Memory) AddDiscoveryRange(_ context.Context, spec, note, by string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.discRanges {
		if r.Spec == spec {
			return 0, ErrRangeExists
		}
	}
	m.discSeq++
	m.discRanges = append(m.discRanges, DiscoveryRange{ID: m.discSeq, Spec: spec, Note: note, Enabled: true,
		CreatedAt: m.now().UTC(), CreatedBy: by})
	return m.discSeq, nil
}

func (m *Memory) SetDiscoveryRangeEnabled(_ context.Context, id int64, enabled bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.discRanges {
		if m.discRanges[i].ID == id {
			m.discRanges[i].Enabled = enabled
			return true, nil
		}
	}
	return false, nil
}

func (m *Memory) DeleteDiscoveryRange(_ context.Context, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.discRanges {
		if m.discRanges[i].ID == id {
			m.discRanges = slices.Delete(m.discRanges, i, i+1)
			return true, nil
		}
	}
	return false, nil
}

func (m *Memory) DiscoveryRanges(_ context.Context) ([]DiscoveryRange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.discRanges), nil
}

func (m *Memory) ReplaceDiscoveryHosts(_ context.Context, hosts []DiscoveryHost, run DiscoveryRun) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := map[string]time.Time{}
	for _, h := range m.discHosts {
		prev[h.IP] = h.FirstSeenAt
	}
	next := make([]DiscoveryHost, 0, len(hosts))
	for _, h := range hosts {
		h.FirstSeenAt = run.StartedAt
		if t, ok := prev[h.IP]; ok {
			h.FirstSeenAt = t
		}
		h.LastSeenAt = run.StartedAt
		next = append(next, h)
	}
	slices.SortFunc(next, func(a, b DiscoveryHost) int { return cmpIP(a.IP, b.IP) })
	m.discHosts = next
	m.discSeq++
	run.ID = m.discSeq
	m.discRuns = append(m.discRuns, run)
	if len(m.discRuns) > 50 {
		m.discRuns = m.discRuns[len(m.discRuns)-50:]
	}
	return run.ID, nil
}

func (m *Memory) DiscoveryHosts(_ context.Context) ([]DiscoveryHost, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.discHosts), nil
}

func (m *Memory) DiscoveryRuns(_ context.Context, limit int) ([]DiscoveryRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := slices.Clone(m.discRuns)
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// cmpIP ordena IPs pelo valor numérico (inet), não como texto.
func cmpIP(a, b string) int {
	x, errA := netip.ParseAddr(a)
	y, errB := netip.ParseAddr(b)
	if errA != nil || errB != nil {
		return cmp.Compare(a, b)
	}
	return x.Compare(y)
}
