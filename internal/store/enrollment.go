package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/identity"
)

// CreateEnrollmentToken guarda um token novo (só o hash) e devolve o ID dele.
func (p *Postgres) CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO enrollment_tokens (token_hash, note, expires_at, max_uses)
		VALUES ($1, $2, $3, $4) RETURNING id`, hash, note, expiresAt, maxUses).Scan(&id)
	return id, err
}

// EnrollmentTokens lista os tokens, do mais novo para o mais antigo.
func (p *Postgres) EnrollmentTokens(ctx context.Context) ([]identity.EnrollmentToken, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, note, created_at, expires_at, max_uses, uses, revoked_at
		FROM enrollment_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []identity.EnrollmentToken{}
	for rows.Next() {
		var t identity.EnrollmentToken
		if err := rows.Scan(&t.ID, &t.Note, &t.CreatedAt, &t.ExpiresAt, &t.MaxUses, &t.Uses, &t.RevokedAt); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt, t.RevokedAt = t.CreatedAt.UTC(), t.ExpiresAt.UTC(), utc(t.RevokedAt)
		list = append(list, t)
	}
	return list, rows.Err()
}

// RevokeEnrollmentToken revoga um token. Devolve false se o ID não existe.
// As máquinas já registradas com ele continuam com as suas credenciais.
func (p *Postgres) RevokeEnrollmentToken(ctx context.Context, id int64) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE id = $1`, id)
	return tag.RowsAffected() == 1, err
}

// Enroll consome um uso do token e registra a máquina, numa transação. A linha do token
// fica travada até o fim: dois registros simultâneos não ultrapassam o limite de usos.
func (p *Postgres) Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) error {
	evidence, err := json.Marshal(nm.Evidence)
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var tok identity.EnrollmentToken
	var now time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, expires_at, max_uses, uses, revoked_at, now()
		FROM enrollment_tokens WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&tok.ID, &tok.ExpiresAt, &tok.MaxUses, &tok.Uses, &tok.RevokedAt, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: token desconhecido", identity.ErrEnrollmentRejected)
	}
	if err != nil {
		return err
	}
	if st := tok.Status(now); st != "válido" {
		return fmt.Errorf("%w: token %d %s", identity.ErrEnrollmentRejected, tok.ID, st)
	}

	if _, err := tx.Exec(ctx, "UPDATE enrollment_tokens SET uses = uses + 1 WHERE id = $1", tok.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO machines (id, credential_hash, enrolled_at, enrollment_token_id, agent_version, evidence)
		VALUES ($1, $2, now(), $3, $4, $5)`,
		nm.ID, nm.CredentialHash, tok.ID, nm.AgentVersion, evidence); err != nil {
		return fmt.Errorf("registro da máquina: %w", err)
	}
	return tx.Commit(ctx)
}

// MachineByCredential devolve a máquina dona da credencial.
func (p *Postgres) MachineByCredential(ctx context.Context, credentialHash []byte) (string, bool, error) {
	var id string
	err := p.pool.QueryRow(ctx, "SELECT id FROM machines WHERE credential_hash = $1", credentialHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
