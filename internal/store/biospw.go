package store

import (
	"context"
	"errors"
	"fmt"
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
	// Aula 21.5: o job que grava a senha (0: nenhum ainda) e o que a máquina respondeu.
	JobID  int64
	Result string
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
	b.State, b.UpdatedAt, b.Reveals, b.LastRevealedAt, b.LastRevealedBy, b.JobID, b.Result = BIOSPending, b.CreatedAt, 0, nil, "", 0, ""
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
	       last_revealed_at, last_revealed_by, coalesce(job_id, 0), result
	FROM bios_passwords`

func scanBIOS(row pgx.Row) (BIOSPassword, bool, error) {
	var b BIOSPassword
	err := row.Scan(&b.ID, &b.MachineID, &b.State, &b.Escrow, &b.EscrowKeyID, &b.CreatedAt, &b.CreatedBy, &b.UpdatedAt,
		&b.Reveals, &b.LastRevealedAt, &b.LastRevealedBy, &b.JobID, &b.Result)
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

// AttachBIOSJob liga a senha pendente ao job que a grava (Aula 21.5). false: ela não está
// mais pendente, ou já tem job (dois cliques): o job novo não deve seguir.
func (p *Postgres) AttachBIOSJob(ctx context.Context, id, jobID int64, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE bios_passwords SET job_id = $2, result = '', updated_at = $3
		WHERE id = $1 AND state = 'pendente' AND job_id IS NULL`, id, jobID, at)
	return tag.RowsAffected() == 1, err
}

// SettleBIOSPassword fecha a gravação da senha pendente pelo job jobID (Aula 21.5):
//   - ativa: esta passa a valer, e a ativa anterior da máquina vira substituída;
//   - falhou ou incerta: fica registrada, com o resultado;
//   - pendente: o job não chegou à BIOS (recusado, expirou sem entrega); a senha volta a
//     esperar, sem o job.
//
// Só vale para a linha ainda pendente com esse job: duas conferências ao mesmo tempo não
// fecham duas vezes. false: nada mudou.
func (p *Postgres) SettleBIOSPassword(ctx context.Context, id, jobID int64, state, result string, at time.Time) (bool, error) {
	if !settleState(state) {
		return false, fmt.Errorf("estado de fechamento inválido %q", state)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var machine string
	err = tx.QueryRow(ctx, `SELECT machine_id FROM bios_passwords WHERE id = $1 AND state = 'pendente' AND job_id = $2 FOR UPDATE`,
		id, jobID).Scan(&machine)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state == BIOSActive {
		if _, err := tx.Exec(ctx, `UPDATE bios_passwords SET state = 'substituida', updated_at = $2 WHERE machine_id = $1 AND state = 'ativa'`,
			machine, at); err != nil {
			return false, err
		}
	}
	q := `UPDATE bios_passwords SET state = $2, result = $3, updated_at = $4 WHERE id = $1`
	if state == BIOSPending {
		q = `UPDATE bios_passwords SET state = $2, result = $3, updated_at = $4, job_id = NULL WHERE id = $1`
	}
	if _, err := tx.Exec(ctx, q, id, state, result, at); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ResolveBIOSPassword decide uma senha incerta depois que alguém conferiu na BIOS (Aula
// 21.5): newValid = a nova vale (vira ativa, e a ativa anterior, substituída); senão, a nova
// fica como falhou e a anterior continua. false: a linha não está incerta.
func (p *Postgres) ResolveBIOSPassword(ctx context.Context, id int64, newValid bool, result string, at time.Time) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var machine string
	err = tx.QueryRow(ctx, `SELECT machine_id FROM bios_passwords WHERE id = $1 AND state = 'incerta' FOR UPDATE`, id).Scan(&machine)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state := BIOSFailed
	if newValid {
		state = BIOSActive
		if _, err := tx.Exec(ctx, `UPDATE bios_passwords SET state = 'substituida', updated_at = $2 WHERE machine_id = $1 AND state = 'ativa'`,
			machine, at); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE bios_passwords SET state = $2, result = $3, updated_at = $4 WHERE id = $1`, id, state, result, at); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func settleState(s string) bool {
	return s == BIOSActive || s == BIOSFailed || s == BIOSUncertain || s == BIOSPending
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
	b.State, b.UpdatedAt, b.Reveals, b.LastRevealedAt, b.LastRevealedBy, b.JobID, b.Result = BIOSPending, b.CreatedAt, 0, nil, "", 0, ""
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

// AttachBIOSJob na memória.
func (m *Memory) AttachBIOSJob(ctx context.Context, id, jobID int64, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || id > int64(len(m.biosPasswords)) {
		return false, nil
	}
	b := &m.biosPasswords[id-1]
	if b.State != BIOSPending || b.JobID != 0 {
		return false, nil
	}
	b.JobID, b.Result, b.UpdatedAt = jobID, "", at
	return true, nil
}

// SettleBIOSPassword na memória.
func (m *Memory) SettleBIOSPassword(ctx context.Context, id, jobID int64, state, result string, at time.Time) (bool, error) {
	if !settleState(state) {
		return false, fmt.Errorf("estado de fechamento inválido %q", state)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || id > int64(len(m.biosPasswords)) {
		return false, nil
	}
	b := &m.biosPasswords[id-1]
	if b.State != BIOSPending || b.JobID != jobID || jobID == 0 {
		return false, nil
	}
	if state == BIOSActive {
		m.replaceActiveBIOS(b.MachineID, at)
	}
	b.State, b.Result, b.UpdatedAt = state, result, at
	if state == BIOSPending {
		b.JobID = 0
	}
	return true, nil
}

// ResolveBIOSPassword na memória.
func (m *Memory) ResolveBIOSPassword(ctx context.Context, id int64, newValid bool, result string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || id > int64(len(m.biosPasswords)) || m.biosPasswords[id-1].State != BIOSUncertain {
		return false, nil
	}
	b := &m.biosPasswords[id-1]
	b.State = BIOSFailed
	if newValid {
		m.replaceActiveBIOS(b.MachineID, at)
		b.State = BIOSActive
	}
	b.Result, b.UpdatedAt = result, at
	return true, nil
}

func (m *Memory) replaceActiveBIOS(machineID string, at time.Time) {
	for i := range m.biosPasswords {
		if c := &m.biosPasswords[i]; c.MachineID == machineID && c.State == BIOSActive {
			c.State, c.UpdatedAt = BIOSReplaced, at
		}
	}
}
