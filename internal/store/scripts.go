package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tasantiago/patchd/internal/bundle"
)

// Bundles de script (Aula 23.8): os metadados do ZIP que o admin subiu. O arquivo fica no
// diretório de conteúdo do servidor (bundles/<sha256>.zip); aqui, só o que a tela precisa.

// ScriptBundle é um bundle cadastrado.
type ScriptBundle struct {
	ID         int64
	Name       string
	SHA256     string
	Size       int64
	OS         string // "windows", "unix" ou "" (serve aos dois)
	Files      []bundle.Entry
	Note       string
	UploadedAt time.Time
	UploadedBy string
}

// Entries: os arquivos do bundle que servem de ponto de entrada, em ordem.
func (b ScriptBundle) Entries() []string {
	var out []string
	for _, f := range b.Files {
		if bundle.RunnerFor(f.Name) != "" {
			out = append(out, f.Name)
		}
	}
	return out
}

// ErrBundleExists: já há um bundle com esse SHA-256 (o mesmo ZIP, subido de novo).
var ErrBundleExists = errors.New("bundle já cadastrado")

// --- Postgres ----------------------------------------------------------------------------

func (p *Postgres) AddScriptBundle(ctx context.Context, b ScriptBundle) (int64, error) {
	files, err := json.Marshal(b.Files)
	if err != nil {
		return 0, err
	}
	var id int64
	err = p.pool.QueryRow(ctx, `
		INSERT INTO script_bundles (name, sha256, size, os, files, note, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (sha256) DO NOTHING RETURNING id`,
		b.Name, b.SHA256, b.Size, b.OS, files, b.Note, b.UploadedBy).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrBundleExists
	}
	return id, err
}

func (p *Postgres) ScriptBundles(ctx context.Context) ([]ScriptBundle, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, name, sha256, size, os, files, note, uploaded_at, uploaded_by
		FROM script_bundles ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanBundle)
}

func (p *Postgres) ScriptBundle(ctx context.Context, id int64) (ScriptBundle, bool, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, name, sha256, size, os, files, note, uploaded_at, uploaded_by
		FROM script_bundles WHERE id = $1`, id)
	if err != nil {
		return ScriptBundle{}, false, err
	}
	b, err := pgx.CollectOneRow(rows, scanBundle)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScriptBundle{}, false, nil
	}
	return b, err == nil, err
}

func (p *Postgres) DeleteScriptBundle(ctx context.Context, id int64) (ScriptBundle, bool, error) {
	b, ok, err := p.ScriptBundle(ctx, id)
	if err != nil || !ok {
		return b, false, err
	}
	tag, err := p.pool.Exec(ctx, `DELETE FROM script_bundles WHERE id = $1`, id)
	return b, tag.RowsAffected() > 0, err
}

func scanBundle(r pgx.CollectableRow) (ScriptBundle, error) {
	var b ScriptBundle
	var files []byte
	if err := r.Scan(&b.ID, &b.Name, &b.SHA256, &b.Size, &b.OS, &files, &b.Note, &b.UploadedAt, &b.UploadedBy); err != nil {
		return b, err
	}
	b.UploadedAt = b.UploadedAt.UTC()
	if len(files) > 0 {
		if err := json.Unmarshal(files, &b.Files); err != nil {
			return b, err
		}
	}
	return b, nil
}

// --- Memória -----------------------------------------------------------------------------

func (m *Memory) AddScriptBundle(_ context.Context, b ScriptBundle) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.bundles {
		if x.SHA256 == b.SHA256 {
			return 0, ErrBundleExists
		}
	}
	m.bundleSeq++
	b.ID = m.bundleSeq
	if b.UploadedAt.IsZero() {
		b.UploadedAt = m.now().UTC()
	}
	m.bundles = append(m.bundles, b)
	return b.ID, nil
}

func (m *Memory) ScriptBundles(_ context.Context) ([]ScriptBundle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := slices.Clone(m.bundles)
	slices.Reverse(out) // mais novo primeiro, como no Postgres
	return out, nil
}

func (m *Memory) ScriptBundle(_ context.Context, id int64) (ScriptBundle, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.bundles {
		if b.ID == id {
			return b, true, nil
		}
	}
	return ScriptBundle{}, false, nil
}

func (m *Memory) DeleteScriptBundle(_ context.Context, id int64) (ScriptBundle, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, b := range m.bundles {
		if b.ID == id {
			m.bundles = slices.Delete(m.bundles, i, i+1)
			return b, true, nil
		}
	}
	return ScriptBundle{}, false, nil
}
