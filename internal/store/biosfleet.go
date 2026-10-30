package store

import (
	"context"
	"slices"
	"time"
)

// A senha padrão da BIOS de cada fabricante (docs/bios-senha.md, seção 10). Só o envelope
// (biospw.SealFleet), como na guarda.

// BIOSFleetPassword é a senha padrão de um fabricante.
type BIOSFleetPassword struct {
	Vendor      string // "hp", "dell" ou "lenovo"
	Escrow      []byte
	EscrowKeyID string
	SetAt       time.Time
	SetBy       string
}

// BIOSFleetPasswords lista as senhas padrão, pelo fabricante.
func (p *Postgres) BIOSFleetPasswords(ctx context.Context) ([]BIOSFleetPassword, error) {
	rows, err := p.pool.Query(ctx, `SELECT vendor, escrow, escrow_key_id, set_at, set_by FROM bios_fleet_passwords ORDER BY vendor`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BIOSFleetPassword
	for rows.Next() {
		var b BIOSFleetPassword
		if err := rows.Scan(&b.Vendor, &b.Escrow, &b.EscrowKeyID, &b.SetAt, &b.SetBy); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetBIOSFleetPassword grava (ou troca) a senha padrão do fabricante.
func (p *Postgres) SetBIOSFleetPassword(ctx context.Context, b BIOSFleetPassword) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO bios_fleet_passwords (vendor, escrow, escrow_key_id, set_at, set_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (vendor) DO UPDATE SET escrow = EXCLUDED.escrow, escrow_key_id = EXCLUDED.escrow_key_id,
			set_at = EXCLUDED.set_at, set_by = EXCLUDED.set_by`,
		b.Vendor, b.Escrow, b.EscrowKeyID, b.SetAt, b.SetBy)
	return err
}

// DeleteBIOSFleetPassword apaga a senha padrão do fabricante.
func (p *Postgres) DeleteBIOSFleetPassword(ctx context.Context, vendor string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM bios_fleet_passwords WHERE vendor = $1`, vendor)
	return tag.RowsAffected() > 0, err
}

// BIOSFleetPasswords na memória.
func (m *Memory) BIOSFleetPasswords(ctx context.Context) ([]BIOSFleetPassword, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BIOSFleetPassword, 0, len(m.biosFleet))
	for _, b := range m.biosFleet {
		b.Escrow = slices.Clone(b.Escrow)
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b BIOSFleetPassword) int {
		switch {
		case a.Vendor < b.Vendor:
			return -1
		case a.Vendor > b.Vendor:
			return 1
		}
		return 0
	})
	return out, nil
}

// SetBIOSFleetPassword na memória.
func (m *Memory) SetBIOSFleetPassword(ctx context.Context, b BIOSFleetPassword) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.biosFleet == nil {
		m.biosFleet = map[string]BIOSFleetPassword{}
	}
	b.Escrow = slices.Clone(b.Escrow)
	m.biosFleet[b.Vendor] = b
	return nil
}

// DeleteBIOSFleetPassword na memória.
func (m *Memory) DeleteBIOSFleetPassword(ctx context.Context, vendor string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, found := m.biosFleet[vendor]
	delete(m.biosFleet, vendor)
	return found, nil
}
