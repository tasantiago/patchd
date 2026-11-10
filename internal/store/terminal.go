package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// O terminal do administrador (docs/terminal.md): as sessões e os comandos.

// TerminalSession: uma sessão aberta por um admin numa máquina, com o motivo.
type TerminalSession struct {
	ID        int64
	MachineID string
	Username  string
	Reason    string
	Remote    string
	OpenedAt  time.Time
	ExpiresAt time.Time
}

// TerminalCommand: um comando do terminal, com o resultado quando voltou.
type TerminalCommand struct {
	ID         int64
	SessionID  int64
	MachineID  string
	Username   string
	Remote     string
	Shell      string
	Command    string
	TimeoutSec int
	JobID      int64 // 0: o job já saiu pela retenção
	CreatedAt  time.Time
	FinishedAt *time.Time
	ExitCode   *int
	Output     string
	Truncated  bool
	Note       string // o que o agente (ou o servidor) disse do resultado
	// Lidos junto: o motivo da sessão e o estado do job.
	Reason   string
	JobState string
}

// TerminalFilter: o recorte do histórico. Vazio: tudo.
type TerminalFilter struct {
	MachineID string
	Username  string
	Limit     int
	Offset    int
}

// TerminalKeep: quanto o registro do terminal fica guardado.
const TerminalKeep = 365 * 24 * time.Hour

// OpenTerminalSession grava a sessão e devolve o ID.
func (p *Postgres) OpenTerminalSession(ctx context.Context, s TerminalSession) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `INSERT INTO terminal_sessions (machine_id, username, reason, remote, opened_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, s.MachineID, s.Username, s.Reason, s.Remote, s.OpenedAt, s.ExpiresAt).Scan(&id)
	return id, err
}

// ActiveTerminalSession: a sessão ainda válida do admin nesta máquina (a mais nova).
func (p *Postgres) ActiveTerminalSession(ctx context.Context, machineID, username string, now time.Time) (TerminalSession, bool, error) {
	var s TerminalSession
	err := p.pool.QueryRow(ctx, `SELECT id, machine_id, username, reason, remote, opened_at, expires_at FROM terminal_sessions
		WHERE machine_id = $1 AND username = $2 AND expires_at > $3 ORDER BY id DESC LIMIT 1`, machineID, username, now).
		Scan(&s.ID, &s.MachineID, &s.Username, &s.Reason, &s.Remote, &s.OpenedAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	s.OpenedAt, s.ExpiresAt = s.OpenedAt.UTC(), s.ExpiresAt.UTC()
	return s, err == nil, err
}

// SetTerminalSessionExpiry muda o fim da sessão (cada comando a renova; fechar a encerra).
func (p *Postgres) SetTerminalSessionExpiry(ctx context.Context, id int64, expires time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE terminal_sessions SET expires_at = $2 WHERE id = $1`, id, expires)
	return err
}

// CreateTerminalCommand grava o comando (antes de o job sair) e devolve o ID.
func (p *Postgres) CreateTerminalCommand(ctx context.Context, c TerminalCommand) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `INSERT INTO terminal_commands (session_id, machine_id, username, remote, shell, command, timeout_sec, job_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		c.SessionID, c.MachineID, c.Username, c.Remote, c.Shell, c.Command, c.TimeoutSec, nullID(c.JobID), c.CreatedAt).Scan(&id)
	return id, err
}

// FinishTerminalCommand grava o resultado do comando do job (uma vez: o primeiro vale).
func (p *Postgres) FinishTerminalCommand(ctx context.Context, jobID int64, exit *int, output string, truncated bool, note string, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE terminal_commands SET finished_at = $2, exit_code = $3, output = $4, truncated = $5, note = $6
		WHERE job_id = $1 AND finished_at IS NULL`, jobID, at, exit, output, truncated, note)
	return tag.RowsAffected() == 1, err
}

// TerminalCommands lista os comandos do recorte, do mais novo para o mais antigo.
func (p *Postgres) TerminalCommands(ctx context.Context, f TerminalFilter) ([]TerminalCommand, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT c.id, c.session_id, c.machine_id, c.username, c.remote, c.shell, c.command, c.timeout_sec, coalesce(c.job_id, 0),
			c.created_at, c.finished_at, c.exit_code, c.output, c.truncated, c.note, s.reason, coalesce(j.state, '')
		FROM terminal_commands c
		JOIN terminal_sessions s ON s.id = c.session_id
		LEFT JOIN jobs j ON j.id = c.job_id
		WHERE ($1 = '' OR c.machine_id = $1) AND ($2 = '' OR c.username = $2)
		ORDER BY c.id DESC LIMIT $3 OFFSET $4`, f.MachineID, f.Username, max(f.Limit, 1), max(f.Offset, 0))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TerminalCommand, error) {
		var c TerminalCommand
		err := r.Scan(&c.ID, &c.SessionID, &c.MachineID, &c.Username, &c.Remote, &c.Shell, &c.Command, &c.TimeoutSec, &c.JobID,
			&c.CreatedAt, &c.FinishedAt, &c.ExitCode, &c.Output, &c.Truncated, &c.Note, &c.Reason, &c.JobState)
		c.CreatedAt, c.FinishedAt = c.CreatedAt.UTC(), utc(c.FinishedAt)
		return c, err
	})
}

// PruneTerminal apaga os comandos mais velhos que before e as sessões que ficaram sem comando.
func (p *Postgres) PruneTerminal(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM terminal_commands WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	_, err = p.pool.Exec(ctx, `DELETE FROM terminal_sessions s WHERE s.expires_at < $1
		AND NOT EXISTS (SELECT 1 FROM terminal_commands c WHERE c.session_id = s.id)`, before)
	return tag.RowsAffected(), err
}

// --- Memória ------------------------------------------------------------------------

type memTerminal struct {
	sessions []TerminalSession
	commands []TerminalCommand
}

func (m *Memory) terminal() *memTerminal {
	if m.terminalData == nil {
		m.terminalData = &memTerminal{}
	}
	return m.terminalData
}

// OpenTerminalSession na memória.
func (m *Memory) OpenTerminalSession(ctx context.Context, s TerminalSession) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.terminal()
	s.ID = int64(len(t.sessions) + 1)
	t.sessions = append(t.sessions, s)
	return s.ID, nil
}

// ActiveTerminalSession na memória.
func (m *Memory) ActiveTerminalSession(ctx context.Context, machineID, username string, now time.Time) (TerminalSession, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.terminalData == nil {
		return TerminalSession{}, false, nil
	}
	for i := len(m.terminalData.sessions) - 1; i >= 0; i-- {
		if s := m.terminalData.sessions[i]; s.MachineID == machineID && s.Username == username && s.ExpiresAt.After(now) {
			return s, true, nil
		}
	}
	return TerminalSession{}, false, nil
}

// SetTerminalSessionExpiry na memória.
func (m *Memory) SetTerminalSessionExpiry(ctx context.Context, id int64, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.terminal().sessions {
		if m.terminalData.sessions[i].ID == id {
			m.terminalData.sessions[i].ExpiresAt = expires
		}
	}
	return nil
}

// CreateTerminalCommand na memória.
func (m *Memory) CreateTerminalCommand(ctx context.Context, c TerminalCommand) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.terminal()
	c.ID = int64(len(t.commands) + 1)
	t.commands = append(t.commands, c)
	return c.ID, nil
}

// FinishTerminalCommand na memória.
func (m *Memory) FinishTerminalCommand(ctx context.Context, jobID int64, exit *int, output string, truncated bool, note string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.terminal()
	for i := range t.commands {
		if c := &t.commands[i]; c.JobID == jobID && c.FinishedAt == nil {
			when := at
			c.FinishedAt, c.ExitCode, c.Output, c.Truncated, c.Note = &when, exit, output, truncated, note
			return true, nil
		}
	}
	return false, nil
}

// TerminalCommands na memória.
func (m *Memory) TerminalCommands(ctx context.Context, f TerminalFilter) ([]TerminalCommand, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.terminalData == nil {
		return nil, nil
	}
	var out []TerminalCommand
	for i := len(m.terminalData.commands) - 1; i >= 0; i-- {
		c := m.terminalData.commands[i]
		if (f.MachineID != "" && c.MachineID != f.MachineID) || (f.Username != "" && !strings.EqualFold(c.Username, f.Username)) {
			continue
		}
		if i := slices.IndexFunc(m.terminalData.sessions, func(s TerminalSession) bool { return s.ID == c.SessionID }); i >= 0 {
			c.Reason = m.terminalData.sessions[i].Reason
		}
		for _, j := range m.jobs {
			if j.ID == c.JobID {
				c.JobState = j.State
			}
		}
		out = append(out, c)
	}
	if f.Offset >= len(out) {
		return nil, nil
	}
	out = out[f.Offset:]
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// PruneTerminal na memória.
func (m *Memory) PruneTerminal(ctx context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.terminal()
	n := len(t.commands)
	t.commands = slices.DeleteFunc(t.commands, func(c TerminalCommand) bool { return c.CreatedAt.Before(before) })
	return int64(n - len(t.commands)), nil
}
