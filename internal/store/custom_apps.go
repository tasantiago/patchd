package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Os programas de terceiros incluídos pelo painel (migração 0074): a regra, no formato do
// manifesto, que o servidor soma ao embutido e ao local. Quem confere a regra é o pacote
// thirdparty; aqui só se guarda.

// CustomApp é um programa incluído pelo painel.
type CustomApp struct {
	ID        string
	Rule      json.RawMessage
	CreatedBy string
	CreatedAt time.Time
	UpdatedBy string
	UpdatedAt time.Time
}

// ErrCustomApp: o programa não está no formato (id vazio, regra que não é um objeto JSON).
var ErrCustomApp = errors.New("programa incluído inválido")

func (a CustomApp) check() error {
	var obj map[string]any
	if a.ID == "" || len(a.ID) > 64 || strings.TrimSpace(a.UpdatedBy) == "" || json.Unmarshal(a.Rule, &obj) != nil {
		return ErrCustomApp
	}
	return nil
}

// CustomApps lista os programas incluídos pelo painel, por id.
func (p *Postgres) CustomApps(ctx context.Context) ([]CustomApp, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, rule, created_by, created_at, updated_by, updated_at FROM thirdparty_custom_apps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomApp
	for rows.Next() {
		var a CustomApp
		if err := rows.Scan(&a.ID, &a.Rule, &a.CreatedBy, &a.CreatedAt, &a.UpdatedBy, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveCustomApp inclui ou troca o programa (o autor da inclusão fica; o da troca, também).
func (p *Postgres) SaveCustomApp(ctx context.Context, a CustomApp) error {
	if err := a.check(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO thirdparty_custom_apps (id, rule, created_by, updated_by) VALUES ($1, $2, $3, $3)
		ON CONFLICT (id) DO UPDATE SET rule = EXCLUDED.rule, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		a.ID, string(a.Rule), a.UpdatedBy)
	return err
}

// DeleteCustomApp tira o programa; false se não havia.
func (p *Postgres) DeleteCustomApp(ctx context.Context, id string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM thirdparty_custom_apps WHERE id = $1`, id)
	return tag.RowsAffected() > 0, err
}

// CustomApps lista os programas incluídos pelo painel.
func (m *Memory) CustomApps(ctx context.Context) ([]CustomApp, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]CustomApp, 0, len(m.customApps))
	for _, a := range m.customApps {
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y CustomApp) int { return strings.Compare(x.ID, y.ID) })
	return out, nil
}

// SaveCustomApp inclui ou troca o programa.
func (m *Memory) SaveCustomApp(ctx context.Context, a CustomApp) error {
	if err := a.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.customApps == nil {
		m.customApps = map[string]CustomApp{}
	}
	now := m.now()
	if old, ok := m.customApps[a.ID]; ok {
		a.CreatedBy, a.CreatedAt = old.CreatedBy, old.CreatedAt
	} else {
		a.CreatedBy, a.CreatedAt = a.UpdatedBy, now
	}
	a.UpdatedAt = now
	a.Rule = slices.Clone(a.Rule)
	m.customApps[a.ID] = a
	return nil
}

// DeleteCustomApp tira o programa.
func (m *Memory) DeleteCustomApp(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.customApps[id]
	delete(m.customApps, id)
	return ok, nil
}
