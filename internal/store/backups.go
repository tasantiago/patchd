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

// RestoreTest é um ensaio de restauração registrado pelo deploy/restaurar.sh --testar
// (Aula 20.8, F4).
type RestoreTest struct {
	FinishedAt time.Time
	File       string // a cópia ensaiada, só o nome
	OK         bool   // as contagens restauradas conferiram com as da cópia
}

// LastRestoreTest devolve o ensaio mais recente; found = false se nenhum foi registrado.
func (p *Postgres) LastRestoreTest(ctx context.Context) (RestoreTest, bool, error) {
	var r RestoreTest
	err := p.pool.QueryRow(ctx, `
		SELECT finished_at, file, ok
		FROM restore_tests ORDER BY finished_at DESC, id DESC LIMIT 1`).Scan(&r.FinishedAt, &r.File, &r.OK)
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreTest{}, false, nil
	}
	if err != nil {
		return RestoreTest{}, false, err
	}
	return r, true, nil
}

// RecordRestoreTest grava um ensaio (o restaurar.sh faz o mesmo INSERT pelo psql).
func (p *Postgres) RecordRestoreTest(ctx context.Context, r RestoreTest) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO restore_tests (finished_at, file, ok) VALUES ($1, $2, $3)`,
		r.FinishedAt, r.File, r.OK)
	return err
}
