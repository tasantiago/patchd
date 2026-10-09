package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/window"
)

// Anéis e janelas de manutenção (Aula 8.3, RF-25).

// Limites do anel: 0 é o piloto, 1 é o padrão de toda máquina nova.
const (
	PilotRing   = 0
	DefaultRing = 1
	MaxRing     = 9
)

// ErrRing: anel fora de 0 a 9.
var ErrRing = fmt.Errorf("anel fora de 0 a %d", MaxRing)

// RingWindow é a janela de um anel, com quem a definiu.
type RingWindow struct {
	Ring      int
	Window    window.Window
	UpdatedBy string
	UpdatedAt time.Time
}

func validRing(r int) error {
	if r < 0 || r > MaxRing {
		return ErrRing
	}
	return nil
}

// SetMachineRing põe a máquina (da frota) no anel. Devolve false se ela não existe ou
// está aposentada.
func (p *Postgres) SetMachineRing(ctx context.Context, id string, ring int) (bool, error) {
	if err := validRing(ring); err != nil {
		return false, err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE machines SET ring = $2 WHERE id = $1 AND retired_at IS NULL`, id, ring)
	return tag.RowsAffected() == 1, err
}

// MachineRing devolve o anel da máquina da frota.
func (p *Postgres) MachineRing(ctx context.Context, id string) (int, bool, error) {
	var r int
	err := p.pool.QueryRow(ctx, `SELECT ring FROM machines WHERE id = $1 AND retired_at IS NULL`, id).Scan(&r)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return r, err == nil, err
}

// RingMachines lista as máquinas da frota no anel, em ordem de ID.
func (p *Postgres) RingMachines(ctx context.Context, ring int) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM machines WHERE ring = $1 AND retired_at IS NULL ORDER BY id COLLATE "C"`, ring)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetWindow grava (ou troca) a janela do anel.
func (p *Postgres) SetWindow(ctx context.Context, ring int, w window.Window, by string) error {
	if err := validRing(ring); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO maintenance_windows (ring, days, start_min, duration_min, timezone, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (ring) DO UPDATE SET days = excluded.days, start_min = excluded.start_min,
			duration_min = excluded.duration_min, timezone = excluded.timezone,
			updated_by = excluded.updated_by, updated_at = now()`,
		ring, int(w.Days), int(w.Start/time.Minute), int(w.Duration/time.Minute), w.Location.String(), by)
	return err
}

// DeleteWindow remove a janela do anel. Os jobs já criados não mudam: a janela deles
// está no payload assinado.
func (p *Postgres) DeleteWindow(ctx context.Context, ring int) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM maintenance_windows WHERE ring = $1`, ring)
	return tag.RowsAffected() == 1, err
}

// Windows lista as janelas, por anel.
func (p *Postgres) Windows(ctx context.Context) ([]RingWindow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT ring, days, start_min, duration_min, timezone, updated_by, updated_at
		FROM maintenance_windows ORDER BY ring`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RingWindow
	for rows.Next() {
		var rw RingWindow
		var days, start, dur int
		var tz string
		if err := rows.Scan(&rw.Ring, &days, &start, &dur, &tz, &rw.UpdatedBy, &rw.UpdatedAt); err != nil {
			return nil, err
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("janela do anel %d: fuso %q: %w", rw.Ring, tz, err)
		}
		rw.Window = window.Window{Days: window.Days(days), Start: time.Duration(start) * time.Minute,
			Duration: time.Duration(dur) * time.Minute, Location: loc}
		rw.UpdatedAt = rw.UpdatedAt.UTC()
		out = append(out, rw)
	}
	return out, rows.Err()
}

// --- Memória -------------------------------------------------------------------------

// SetMachineRing põe a máquina no anel.
func (m *Memory) SetMachineRing(ctx context.Context, id string, ring int) (bool, error) {
	if err := validRing(ring); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	if mm == nil || !mm.retiredAt.IsZero() {
		return false, nil
	}
	mm.ring = ring
	return true, nil
}

// MachineRing devolve o anel da máquina da frota.
func (m *Memory) MachineRing(ctx context.Context, id string) (int, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil || !mm.retiredAt.IsZero() {
		return 0, false, nil
	}
	return mm.ring, true, nil
}

// RingMachines lista as máquinas da frota no anel.
func (m *Memory) RingMachines(ctx context.Context, ring int) ([]string, error) {
	var out []string
	for _, s := range m.summaries(false) {
		if s.Ring == ring {
			out = append(out, s.ID)
		}
	}
	return out, nil
}

// SetWindow grava a janela do anel.
func (m *Memory) SetWindow(ctx context.Context, ring int, w window.Window, by string) error {
	if err := validRing(ring); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.windows == nil {
		m.windows = map[int]RingWindow{}
	}
	m.windows[ring] = RingWindow{Ring: ring, Window: w, UpdatedBy: by, UpdatedAt: m.now()}
	return nil
}

// DeleteWindow remove a janela do anel.
func (m *Memory) DeleteWindow(ctx context.Context, ring int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.windows[ring]
	delete(m.windows, ring)
	return ok, nil
}

// Windows lista as janelas, por anel.
func (m *Memory) Windows(ctx context.Context) ([]RingWindow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []RingWindow
	for r := 0; r <= MaxRing; r++ {
		if w, ok := m.windows[r]; ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// WindowFor acha a janela do anel na lista.
func WindowFor(list []RingWindow, ring int) (RingWindow, bool) {
	for _, w := range list {
		if w.Ring == ring {
			return w, true
		}
	}
	return RingWindow{}, false
}
