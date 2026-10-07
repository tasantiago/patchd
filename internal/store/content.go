package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
)

// ContentFile devolve a versão atual de um arquivo distribuído (ex.: "wsusscn2").
func (p *Postgres) ContentFile(ctx context.Context, name string) (wsusscan.File, bool, error) {
	var f wsusscan.File
	var lastMod *time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT file, size, sha256, etag, last_modified, cabinet FROM content_files WHERE name = $1`, name).
		Scan(&f.Name, &f.Size, &f.SHA256, &f.ETag, &lastMod, &f.Cabinet)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	if lastMod != nil {
		f.LastModified = lastMod.UTC()
	}
	return f, true, nil
}

// SaveContentFile grava a versão atual de um arquivo distribuído.
func (p *Postgres) SaveContentFile(ctx context.Context, name string, f wsusscan.File) error {
	var lastMod *time.Time
	if !f.LastModified.IsZero() {
		lastMod = &f.LastModified
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO content_files (name, file, size, sha256, etag, last_modified, cabinet) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET file = EXCLUDED.file, size = EXCLUDED.size, sha256 = EXCLUDED.sha256,
		       etag = EXCLUDED.etag, last_modified = EXCLUDED.last_modified, cabinet = EXCLUDED.cabinet, fetched_at = now()`,
		name, f.Name, f.Size, f.SHA256, f.ETag, lastMod, f.Cabinet)
	return err
}
