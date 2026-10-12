package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// BackupRun é uma cópia do banco registrada pelo deploy/backup.sh (Aula 10.3).
type BackupRun struct {
	FinishedAt time.Time
	File       string // só o nome, sem a pasta
	Bytes      int64
	Encrypted  bool
}

// LastBackup devolve a cópia mais recente; found = false se nenhuma foi registrada.
func (p *Postgres) LastBackup(ctx context.Context) (BackupRun, bool, error) {
	var b BackupRun
	err := p.pool.QueryRow(ctx, `
		SELECT finished_at, file, bytes, encrypted
		FROM backup_runs ORDER BY finished_at DESC, id DESC LIMIT 1`).Scan(&b.FinishedAt, &b.File, &b.Bytes, &b.Encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackupRun{}, false, nil
	}
	if err != nil {
		return BackupRun{}, false, err
	}
	return b, true, nil
}

// RecordBackup grava uma cópia (o backup.sh faz o mesmo INSERT pelo psql; aqui, para os
// testes e para quem quiser registrar por Go).
func (p *Postgres) RecordBackup(ctx context.Context, b BackupRun) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO backup_runs (finished_at, file, bytes, encrypted) VALUES ($1, $2, $3, $4)`,
		b.FinishedAt, b.File, b.Bytes, b.Encrypted)
	return err
}
