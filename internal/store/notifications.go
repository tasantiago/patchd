package store

import (
	"context"
	"time"
)

// Notificações mandadas (Aula 18.6): a chave de cada aviso, para não repetir.

// NotificationsSent diz quais das chaves já foram mandadas.
func (p *Postgres) NotificationsSent(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT key FROM notifications_sent WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// MarkNotificationsSent guarda as chaves (as que já estavam ficam como estão).
func (p *Postgres) MarkNotificationsSent(ctx context.Context, keys []string, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO notifications_sent (key, sent_at) SELECT unnest($1::text[]), $2 ON CONFLICT (key) DO NOTHING`, keys, at)
	return err
}

// PruneNotifications apaga as chaves mandadas antes de before.
func (p *Postgres) PruneNotifications(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM notifications_sent WHERE sent_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// NotificationsSent: na memória.
func (m *Memory) NotificationsSent(ctx context.Context, keys []string) (map[string]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]bool{}
	for _, k := range keys {
		if _, ok := m.notified[k]; ok {
			out[k] = true
		}
	}
	return out, nil
}

// MarkNotificationsSent: na memória.
func (m *Memory) MarkNotificationsSent(ctx context.Context, keys []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.notified == nil {
		m.notified = map[string]time.Time{}
	}
	for _, k := range keys {
		if _, ok := m.notified[k]; !ok {
			m.notified[k] = at
		}
	}
	return nil
}

// PruneNotifications: na memória.
func (m *Memory) PruneNotifications(ctx context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, t := range m.notified {
		if t.Before(before) {
			delete(m.notified, k)
			n++
		}
	}
	return n, nil
}
