package store

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Parâmetros mudados pela tela (Aula 19.2): o valor que vale no lugar do .env.

// Setting é um parâmetro gravado pela tela.
type Setting struct {
	Key       string // a variável do .env (PATCHD_...)
	Value     string
	UpdatedAt time.Time
	UpdatedBy string
}

var settingKey = regexp.MustCompile(`^PATCHD_[A-Z0-9_]{1,60}$`)

// MaxSettingValue: o maior valor aceito (o mesmo limite da migração 0038).
const MaxSettingValue = 2000

func checkSetting(s Setting) error {
	switch {
	case !settingKey.MatchString(s.Key):
		return fmt.Errorf("parâmetro inválido: %q", s.Key)
	case len(s.Value) > MaxSettingValue:
		return fmt.Errorf("%s: valor grande demais (%d bytes; máximo %d)", s.Key, len(s.Value), MaxSettingValue)
	case strings.ContainsAny(s.Value, "\x00\r\n"):
		return fmt.Errorf("%s: o valor não pode ter quebra de linha", s.Key)
	}
	return nil
}

// Settings devolve os parâmetros gravados, pela chave.
func (p *Postgres) Settings(ctx context.Context) ([]Setting, error) {
	rows, err := p.pool.Query(ctx, `SELECT key, value, updated_at, updated_by FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var s Setting
		if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt, &s.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetSettings grava os parâmetros juntos: ou todos, ou nenhum.
func (p *Postgres) SetSettings(ctx context.Context, list []Setting) error {
	for _, s := range list {
		if err := checkSetting(s); err != nil {
			return err
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, s := range list {
		if _, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at, updated_by) VALUES ($1, $2, now(), $3)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
			s.Key, s.Value, s.UpdatedBy); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteSetting apaga o parâmetro (o .env volta a valer); false quando não havia.
func (p *Postgres) DeleteSetting(ctx context.Context, key string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM settings WHERE key = $1`, key)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Settings: na memória.
func (m *Memory) Settings(ctx context.Context) ([]Setting, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Setting, 0, len(m.settings))
	for _, s := range m.settings {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Setting) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// SetSettings: na memória.
func (m *Memory) SetSettings(ctx context.Context, list []Setting) error {
	for _, s := range list {
		if err := checkSetting(s); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings == nil {
		m.settings = map[string]Setting{}
	}
	for _, s := range list {
		s.UpdatedAt = m.now().UTC()
		m.settings[s.Key] = s
	}
	return nil
}

// DeleteSetting: na memória.
func (m *Memory) DeleteSetting(ctx context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.settings[key]
	delete(m.settings, key)
	return ok, nil
}
