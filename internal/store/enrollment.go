package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// enrollLockID serializa os registros. Sem ele, duas máquinas clonadas registrando-se no
// mesmo instante, com tokens diferentes, não enxergariam uma à outra (cada transação só vê
// o que já foi confirmado) e o clone passaria sem alerta. Registro é raro: a fila não pesa.
const enrollLockID int64 = 7_243_042_002

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

// nullable converte texto vazio em NULL: evidência ausente nunca casa com nada.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Enroll consome um uso do token, registra a máquina e cria os alertas de identidade
// contra as máquinas já conhecidas, numa transação. Devolve os alertas criados.
func (p *Postgres) Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) ([]protocol.IdentityLink, error) {
	evidence, err := json.Marshal(nm.Evidence)
	if err != nil {
		return nil, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", enrollLockID); err != nil {
		return nil, err
	}

	var tok identity.EnrollmentToken
	var now time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, expires_at, max_uses, uses, revoked_at, now()
		FROM enrollment_tokens WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&tok.ID, &tok.ExpiresAt, &tok.MaxUses, &tok.Uses, &tok.RevokedAt, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: token desconhecido", identity.ErrEnrollmentRejected)
	}
	if err != nil {
		return nil, err
	}
	if st := tok.Status(now); st != "válido" {
		return nil, fmt.Errorf("%w: token %d %s", identity.ErrEnrollmentRejected, tok.ID, st)
	}

	// Candidatas: qualquer máquina com alguma chave igual. A relação é decidida em Go,
	// pela mesma função que os testes conferem (identity.Relation).
	k := nm.Keys
	rows, err := tx.Query(ctx, `
		SELECT id, coalesce(install_id, ''), coalesce(hardware_key, ''), coalesce(serial, '')
		FROM machines
		WHERE install_id = $1 OR hardware_key = $2 OR serial = $3
		ORDER BY first_seen_at, id`,
		nullable(k.InstallID), nullable(k.HardwareKey), nullable(k.Serial))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id   string
		keys identity.Keys
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.keys.InstallID, &c.keys.HardwareKey, &c.keys.Serial); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, "UPDATE enrollment_tokens SET uses = uses + 1 WHERE id = $1", tok.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO machines (id, credential_hash, enrolled_at, enrollment_token_id, agent_version, evidence,
		                      install_id, hardware_key, serial)
		VALUES ($1, $2, now(), $3, $4, $5, $6, $7, $8)`,
		nm.ID, nm.CredentialHash, tok.ID, nm.AgentVersion, evidence,
		nullable(k.InstallID), nullable(k.HardwareKey), nullable(k.Serial)); err != nil {
		return nil, fmt.Errorf("registro da máquina: %w", err)
	}

	links := []protocol.IdentityLink{}
	for _, c := range candidates {
		rel, ok := identity.Relation(k, c.keys)
		if !ok {
			continue
		}
		l := protocol.IdentityLink{MachineID: nm.ID, RelatedMachineID: c.id, Relation: rel}
		if err := tx.QueryRow(ctx, `
			INSERT INTO identity_links (machine_id, related_machine_id, relation)
			VALUES ($1, $2, $3) RETURNING id, created_at`,
			l.MachineID, l.RelatedMachineID, l.Relation).Scan(&l.ID, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("alerta de identidade: %w", err)
		}
		l.CreatedAt = l.CreatedAt.UTC()
		links = append(links, l)
	}
	return links, tx.Commit(ctx)
}

// IdentityLinks lista os alertas de identidade, do mais antigo para o mais novo.
func (p *Postgres) IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, related_machine_id, relation, created_at
		FROM identity_links ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []protocol.IdentityLink{}
	for rows.Next() {
		var l protocol.IdentityLink
		if err := rows.Scan(&l.ID, &l.MachineID, &l.RelatedMachineID, &l.Relation, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.CreatedAt = l.CreatedAt.UTC()
		list = append(list, l)
	}
	return list, rows.Err()
}

// MachineByCredential devolve a máquina dona da credencial. A de uma máquina aposentada
// não vale (Aula 7.5): o agente dela recebe 401 até a máquina ser restaurada.
func (p *Postgres) MachineByCredential(ctx context.Context, credentialHash []byte) (string, bool, error) {
	var id string
	err := p.pool.QueryRow(ctx, "SELECT id FROM machines WHERE credential_hash = $1 AND retired_at IS NULL", credentialHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
