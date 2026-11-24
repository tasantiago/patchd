package store

import (
	"context"
	"strings"
	"time"
)

// UserDisplayNames: o nome de exibição (o do AD, que o agente lê) de cada conta conectada nas
// máquinas, pela última coleta de cada uma. A chave é a conta em minúsculas (DOMINIO\usuario).
// Quem não está conectado em máquina nenhuma agora fica de fora: as telas mostram só a conta.
func (p *Postgres) UserDisplayNames(ctx context.Context) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT ON (lower(u->>'account')) u->>'account', u->>'display_name'
		FROM machines m,
		     jsonb_array_elements(CASE WHEN jsonb_typeof(m.state->'users') = 'array' THEN m.state->'users' ELSE '[]'::jsonb END) u
		WHERE m.retired_at IS NULL AND coalesce(u->>'account', '') <> '' AND coalesce(u->>'display_name', '') <> ''
		ORDER BY lower(u->>'account'), m.state_at DESC NULLS LAST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var acc, name string
		if err := rows.Scan(&acc, &name); err != nil {
			return nil, err
		}
		out[strings.ToLower(acc)] = name
	}
	return out, rows.Err()
}

// UserDisplayNames (memória).
func (m *Memory) UserDisplayNames(context.Context) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]string{}
	at := map[string]time.Time{}
	for _, mm := range m.machines {
		if mm.state == nil || !mm.retiredAt.IsZero() {
			continue
		}
		for _, u := range mm.state.Users {
			k := strings.ToLower(u.Account)
			if k == "" || u.DisplayName == "" {
				continue
			}
			if prev, ok := at[k]; ok && !mm.stateAt.After(prev) {
				continue
			}
			out[k], at[k] = u.DisplayName, mm.stateAt
		}
	}
	return out, nil
}
