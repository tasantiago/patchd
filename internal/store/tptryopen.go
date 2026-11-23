package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// O "tentar com o programa aberto" de cada programa de terceiros (migração 0081): a escolha
// do painel por cima da regra. Sem linha: vale a regra.

// TryOpenSetting é a escolha de um programa.
type TryOpenSetting struct {
	AppID     string
	Enabled   bool
	UpdatedBy string
	UpdatedAt time.Time
}

// ErrTryOpen: a escolha fora do formato.
var ErrTryOpen = errors.New("tentar com o programa aberto: escolha inválida")

func (s TryOpenSetting) check() error {
	if s.AppID == "" || len(s.AppID) > 64 || strings.TrimSpace(s.UpdatedBy) == "" {
		return ErrTryOpen
	}
	return nil
}

// TryOpenSettings: as escolhas gravadas, por programa (id → ligado).
func (p *Postgres) TryOpenSettings(ctx context.Context) (map[string]bool, error) {
	rows, err := p.pool.Query(ctx, `SELECT app_id, enabled FROM thirdparty_try_open`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var on bool
		if err := rows.Scan(&id, &on); err != nil {
			return nil, err
		}
		out[id] = on
	}
	return out, rows.Err()
}

// SaveTryOpen grava (ou troca) a escolha do programa.
func (p *Postgres) SaveTryOpen(ctx context.Context, s TryOpenSetting) error {
	if err := s.check(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO thirdparty_try_open (app_id, enabled, updated_by, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (app_id) DO UPDATE SET enabled = $2, updated_by = $3, updated_at = now()`, s.AppID, s.Enabled, s.UpdatedBy)
	return err
}

// TryOpenSettings (memória).
func (m *Memory) TryOpenSettings(context.Context) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for id, s := range m.tryOpen {
		out[id] = s.Enabled
	}
	return out, nil
}

// SaveTryOpen (memória).
func (m *Memory) SaveTryOpen(_ context.Context, s TryOpenSetting) error {
	if err := s.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tryOpen == nil {
		m.tryOpen = map[string]TryOpenSetting{}
	}
	s.UpdatedAt = m.now()
	m.tryOpen[s.AppID] = s
	return nil
}
