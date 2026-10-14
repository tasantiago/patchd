package store

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/panel"
)

// Sessões abertas do painel (Aula 15.4): a lista na Configuração › Acesso e o encerramento
// pelo admin. Tirar alguém de um grupo do AD não derruba a sessão que ele já tem: o
// encerramento é o que fecha a porta agora.

// PanelSessionRow é uma sessão, com o hash do segredo (nunca o segredo) como identificador.
type PanelSessionRow struct {
	Hash []byte
	panel.Session
}

// PanelSessions lista as sessões, da usada mais recentemente para a mais antiga.
func (p *Postgres) PanelSessions(ctx context.Context) ([]PanelSessionRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT token_hash, username, role, source, created_at, expires_at, last_seen_at
		FROM panel_sessions ORDER BY last_seen_at DESC, username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PanelSessionRow, error) {
		var s PanelSessionRow
		err := r.Scan(&s.Hash, &s.Username, &s.Role, &s.Source, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt)
		s.CreatedAt, s.ExpiresAt, s.LastSeenAt = s.CreatedAt.UTC(), s.ExpiresAt.UTC(), s.LastSeenAt.UTC()
		return s, err
	})
}

// DeleteUserSessions encerra todas as sessões de um usuário, de qualquer origem.
func (p *Postgres) DeleteUserSessions(ctx context.Context, username string) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM panel_sessions WHERE username = $1`, username)
	return tag.RowsAffected(), err
}

// PanelSessions na memória.
func (m *Memory) PanelSessions(ctx context.Context) ([]PanelSessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PanelSessionRow, 0, len(m.panelSessions))
	for k, s := range m.panelSessions {
		out = append(out, PanelSessionRow{Hash: []byte(k), Session: s})
	}
	slices.SortFunc(out, func(a, b PanelSessionRow) int {
		if c := b.LastSeenAt.Compare(a.LastSeenAt); c != 0 {
			return c
		}
		return bytes.Compare(a.Hash, b.Hash)
	})
	return out, nil
}

// DeleteUserSessions na memória.
func (m *Memory) DeleteUserSessions(ctx context.Context, username string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.panelSessions {
		if s.Username == username {
			delete(m.panelSessions, k)
			n++
		}
	}
	return n, nil
}

// --- Contagem dos jobs (Aula 15.4) ------------------------------------------------

// JobCount: quantos jobs de um tipo estão num estado.
type JobCount struct {
	Type, State string
	N           int
}

// JobCounts conta, no banco, os jobs criados a partir de since, por tipo e estado. A
// Visão geral lia os 2.000 mais recentes e contava na memória: numa semana com mais que
// isso (campanha grande, novas tentativas), a conta ficava menor, sem aviso.
func (p *Postgres) JobCounts(ctx context.Context, since time.Time) ([]JobCount, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT type, state, count(*) FROM jobs WHERE created_at >= $1 GROUP BY type, state ORDER BY type, state`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (JobCount, error) {
		var c JobCount
		err := r.Scan(&c.Type, &c.State, &c.N)
		return c, err
	})
}

// JobsFinishedSince conta os jobs que terminaram num dos estados a partir de since.
func (p *Postgres) JobsFinishedSince(ctx context.Context, states []string, since time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state = ANY($1) AND finished_at >= $2`, states, since).Scan(&n)
	return n, err
}

// JobCounts na memória.
func (m *Memory) JobCounts(ctx context.Context, since time.Time) ([]JobCount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	idx := map[[2]string]int{}
	var out []JobCount
	for _, j := range m.jobs {
		if j.CreatedAt.Before(since) {
			continue
		}
		k := [2]string{j.Type, j.State}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, JobCount{Type: j.Type, State: j.State})
		}
		out[i].N++
	}
	slices.SortFunc(out, func(a, b JobCount) int { return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.State, b.State)) })
	return out, nil
}

// JobsFinishedSince na memória.
func (m *Memory) JobsFinishedSince(ctx context.Context, states []string, since time.Time) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, j := range m.jobs {
		if j.FinishedAt != nil && !j.FinishedAt.Before(since) && slices.Contains(states, j.State) {
			n++
		}
	}
	return n, nil
}
