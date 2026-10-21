package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Guarda da senha da BIOS (Aula 21.4). O store só guarda envelopes: quem fecha e abre é o
// pacote biospw, com a chave da guarda, que nunca vem para o banco.

// Estados da senha guardada.
const (
	BIOSPending   = "pendente"    // guardada, ainda não gravada na BIOS
	BIOSActive    = "ativa"       // a máquina confirmou a gravação
	BIOSUncertain = "incerta"     // a gravação saiu e a resposta não voltou
	BIOSFailed    = "falhou"      // a máquina recusou (a senha anterior continua)
	BIOSReplaced  = "substituida" // outra ficou ativa depois desta
)

// BIOSPassword é uma senha guardada.
type BIOSPassword struct {
	ID             int64
	MachineID      string
	State          string
	Escrow         []byte // o envelope (biospw.Seal)
	EscrowKeyID    string // a impressão da chave de guarda que fechou
	CreatedAt      time.Time
	CreatedBy      string
	UpdatedAt      time.Time
	Reveals        int
	LastRevealedAt *time.Time
	LastRevealedBy string
}

// ErrNoMachine: a máquina não existe ou está aposentada.
var ErrNoMachine = errors.New("máquina fora da frota")

// CreateBIOSPassword guarda uma senha pendente. Se a máquina já tem uma pendente, devolve
// essa (created = false) e não grava a nova: a pendente é a que a gravação vai usar.
func (p *Postgres) CreateBIOSPassword(ctx context.Context, b BIOSPassword) (BIOSPassword, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return BIOSPassword{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A trava da linha da máquina serializa dois pedidos ao mesmo tempo (dois cliques).
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM machines WHERE id = $1 AND retired_at IS NULL FOR UPDATE`, b.MachineID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return BIOSPassword{}, false, ErrNoMachine
	}
	if err != nil {
		return BIOSPassword{}, false, err
	}
	cur, found, err := scanBIOS(tx.QueryRow(ctx, biosSelect+` WHERE machine_id = $1 AND state = 'pendente'`, b.MachineID))
	if err != nil || found {
		return cur, false, err
	}
	b.State, b.UpdatedAt, b.Reveals, b.LastRevealedAt, b.LastRevealedBy = BIOSPending, b.CreatedAt, 0, nil, ""
	err = tx.QueryRow(ctx, `
		INSERT INTO bios_passwords (machine_id, state, escrow, escrow_key_id, created_at, created_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $5) RETURNING id`,
		b.MachineID, b.State, b.Escrow, b.EscrowKeyID, b.CreatedAt, b.CreatedBy).Scan(&b.ID)
	if err != nil {
		return BIOSPassword{}, false, err
	}
	return b, true, tx.Commit(ctx)
}

const biosSelect = `
	SELECT id, machine_id, state, escrow, escrow_key_id, created_at, created_by, updated_at, reveals,
	       last_revealed_at, last_revealed_by
	FROM bios_passwords`

func scanBIOS(row pgx.Row) (BIOSPassword, bool, error) {
	var b BIOSPassword
	err := row.Scan(&b.ID, &b.MachineID, &b.State, &b.Escrow, &b.EscrowKeyID, &b.CreatedAt, &b.CreatedBy, &b.UpdatedAt,
		&b.Reveals, &b.LastRevealedAt, &b.LastRevealedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return BIOSPassword{}, false, nil
	}
	return b, err == nil, err
}

// BIOSPasswords lista as senhas guardadas da máquina, da mais nova para a mais antiga.
func (p *Postgres) BIOSPasswords(ctx context.Context, machineID string) ([]BIOSPassword, error) {
	rows, err := p.pool.Query(ctx, biosSelect+` WHERE machine_id = $1 ORDER BY id DESC`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BIOSPassword
	for rows.Next() {
		b, _, err := scanBIOS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BIOSPasswordByID devolve uma senha guardada.
func (p *Postgres) BIOSPasswordByID(ctx context.Context, id int64) (BIOSPassword, bool, error) {
	return scanBIOS(p.pool.QueryRow(ctx, biosSelect+` WHERE id = $1`, id))
}

// MarkBIOSRevealed conta uma revelação (quem e quando; o motivo fica na auditoria).
func (p *Postgres) MarkBIOSRevealed(ctx context.Context, id int64, by string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE bios_passwords SET reveals = reveals + 1, last_revealed_at = $2, last_revealed_by = $3 WHERE id = $1`,
		id, at, by)
	return err
}

// --- Memória ------------------------------------------------------------------------------

// CreateBIOSPassword na memória.
func (m *Memory) CreateBIOSPassword(ctx context.Context, b BIOSPassword) (BIOSPassword, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.machines[b.MachineID]; mm == nil || !mm.retiredAt.IsZero() {
		return BIOSPassword{}, false, ErrNoMachine
	}
	for _, c := range m.biosPasswords {
		if c.MachineID == b.MachineID && c.State == BIOSPending {
			return cloneBIOS(c), false, nil
		}
	}
	b.ID = int64(len(m.biosPasswords) + 1)
	b.State, b.UpdatedAt, b.Reveals, b.LastRevealedAt, b.LastRevealedBy = BIOSPending, b.CreatedAt, 0, nil, ""
	b.Escrow = slices.Clone(b.Escrow)
	m.biosPasswords = append(m.biosPasswords, b)
	return cloneBIOS(b), true, nil
}

// BIOSPasswords na memória.
func (m *Memory) BIOSPasswords(ctx context.Context, machineID string) ([]BIOSPassword, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []BIOSPassword
	for i := len(m.biosPasswords) - 1; i >= 0; i-- {
		if b := m.biosPasswords[i]; b.MachineID == machineID {
			out = append(out, cloneBIOS(b))
		}
	}
	return out, nil
}

// BIOSPasswordByID na memória.
func (m *Memory) BIOSPasswordByID(ctx context.Context, id int64) (BIOSPassword, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id < 1 || id > int64(len(m.biosPasswords)) {
		return BIOSPassword{}, false, nil
	}
	return cloneBIOS(m.biosPasswords[id-1]), true, nil
}

// MarkBIOSRevealed na memória.
func (m *Memory) MarkBIOSRevealed(ctx context.Context, id int64, by string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id >= 1 && id <= int64(len(m.biosPasswords)) {
		b := &m.biosPasswords[id-1]
		b.Reveals++
		b.LastRevealedAt, b.LastRevealedBy = &at, by
	}
	return nil
}

func cloneBIOS(b BIOSPassword) BIOSPassword {
	b.Escrow = slices.Clone(b.Escrow)
	if b.LastRevealedAt != nil {
		t := *b.LastRevealedAt
		b.LastRevealedAt = &t
	}
	return b
}
