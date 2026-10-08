package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tasantiago/patchd/internal/panel"
)

// pgUniqueViolation é o SQLSTATE de chave duplicada (PostgreSQL, apêndice A).
const pgUniqueViolation = "23505"

// CreatePanelUser cria um usuário local. Nome repetido devolve panel.ErrUserExists.
func (p *Postgres) CreatePanelUser(ctx context.Context, u panel.User) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO panel_users (username, role, password_hash) VALUES ($1, $2, $3)`,
		u.Name, u.Role, u.PasswordHash)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return panel.ErrUserExists
	}
	return err
}

// PanelUser devolve o usuário local, com o hash da senha.
func (p *Postgres) PanelUser(ctx context.Context, name string) (panel.User, bool, error) {
	u := panel.User{Name: name}
	err := p.pool.QueryRow(ctx, `SELECT role, password_hash, created_at FROM panel_users WHERE username = $1`, name).
		Scan(&u.Role, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return panel.User{}, false, nil
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, err == nil, err
}

// PanelUsers lista os usuários locais em ordem de nome, sem os hashes.
func (p *Postgres) PanelUsers(ctx context.Context) ([]panel.User, error) {
	rows, err := p.pool.Query(ctx, `SELECT username, role, created_at FROM panel_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []panel.User{}
	for rows.Next() {
		var u panel.User
		if err := rows.Scan(&u.Name, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.CreatedAt = u.CreatedAt.UTC()
		list = append(list, u)
	}
	return list, rows.Err()
}

// SetPanelPassword troca a senha e encerra as sessões locais do usuário, na mesma transação.
func (p *Postgres) SetPanelPassword(ctx context.Context, name, hash string) (bool, error) {
	return p.changeUser(ctx, name, `UPDATE panel_users SET password_hash = $2 WHERE username = $1`, hash)
}

// DeletePanelUser remove o usuário local e encerra as sessões locais dele.
func (p *Postgres) DeletePanelUser(ctx context.Context, name string) (bool, error) {
	return p.changeUser(ctx, name, `DELETE FROM panel_users WHERE username = $1`)
}

func (p *Postgres) changeUser(ctx context.Context, name, query string, args ...any) (bool, error) {
	var found bool
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, query, append([]any{name}, args...)...)
		if err != nil {
			return err
		}
		found = tag.RowsAffected() == 1
		_, err = tx.Exec(ctx, `DELETE FROM panel_sessions WHERE username = $1 AND source = 'local'`, name)
		return err
	})
	return found, err
}

// CreatePanelSession guarda a sessão pelo hash do segredo.
func (p *Postgres) CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO panel_sessions (token_hash, username, role, source, created_at, expires_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tokenHash, s.Username, s.Role, s.Source, s.CreatedAt, s.ExpiresAt, s.LastSeenAt)
	return err
}

// PanelSession devolve a sessão, vencida ou não: quem decide é quem chama (panel.Session.Valid).
func (p *Postgres) PanelSession(ctx context.Context, tokenHash []byte) (panel.Session, bool, error) {
	var s panel.Session
	err := p.pool.QueryRow(ctx, `
		SELECT username, role, source, created_at, expires_at, last_seen_at FROM panel_sessions WHERE token_hash = $1`,
		tokenHash).Scan(&s.Username, &s.Role, &s.Source, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return panel.Session{}, false, nil
	}
	if err != nil {
		return panel.Session{}, false, err
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = s.CreatedAt.UTC(), s.ExpiresAt.UTC(), s.LastSeenAt.UTC()
	return s, true, nil
}

// TouchPanelSession registra o uso da sessão (o prazo de inatividade recomeça).
func (p *Postgres) TouchPanelSession(ctx context.Context, tokenHash []byte, at time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE panel_sessions SET last_seen_at = $2 WHERE token_hash = $1`, tokenHash, at)
	return err
}

// DeletePanelSession encerra a sessão (saída do painel ou sessão vencida).
func (p *Postgres) DeletePanelSession(ctx context.Context, tokenHash []byte) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM panel_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteExpiredPanelSessions apaga as sessões vencidas em now (prazo absoluto ou inatividade).
func (p *Postgres) DeleteExpiredPanelSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM panel_sessions WHERE expires_at <= $1 OR last_seen_at <= $2`,
		now, now.Add(-panel.SessionIdle))
	return tag.RowsAffected(), err
}
