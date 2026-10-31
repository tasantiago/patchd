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
	Entry      string   // Run padrão; vazio em cadastros antigos ou sem entrada escolhida
	Args       []string // parâmetros padrão, separados em tokens
	Timeout    int      // segundos; zero em memória equivale ao padrão de 600
}

func (b ScriptBundle) ExecutionTimeout() int {
	if b.Timeout == 0 {
		return 600
	}
	return b.Timeout
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
	args, err := json.Marshal(nonNilScriptArgs(b.Args))
	if err != nil {
		return 0, err
	}
	var id int64
	err = p.pool.QueryRow(ctx, `
		INSERT INTO script_bundles (name, sha256, size, os, files, note, uploaded_by, entry, args, timeout_s)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (sha256) DO NOTHING RETURNING id`,
		b.Name, b.SHA256, b.Size, b.OS, files, b.Note, b.UploadedBy, b.Entry, args, b.ExecutionTimeout()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrBundleExists
	}
	return id, err
}

func (p *Postgres) ScriptBundles(ctx context.Context) ([]ScriptBundle, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, name, sha256, size, os, files, note, uploaded_at, uploaded_by, entry, args, timeout_s
		FROM script_bundles ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanBundle)
}

func (p *Postgres) ScriptBundle(ctx context.Context, id int64) (ScriptBundle, bool, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, name, sha256, size, os, files, note, uploaded_at, uploaded_by, entry, args, timeout_s
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

func (p *Postgres) SetScriptExecution(ctx context.Context, id int64, entry string, args []string, timeout int) (bool, error) {
	raw, err := json.Marshal(nonNilScriptArgs(args))
	if err != nil {
		return false, err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE script_bundles SET entry = $2, args = $3, timeout_s = $4 WHERE id = $1`,
		id, entry, raw, timeout)
	return tag.RowsAffected() > 0, err
}

func nonNilScriptArgs(args []string) []string {
	if args == nil {
		return []string{}
	}
	return args
}

func scanBundle(r pgx.CollectableRow) (ScriptBundle, error) {
	var b ScriptBundle
	var files, args []byte
	if err := r.Scan(&b.ID, &b.Name, &b.SHA256, &b.Size, &b.OS, &files, &b.Note, &b.UploadedAt, &b.UploadedBy, &b.Entry, &args, &b.Timeout); err != nil {
		return b, err
	}
	b.UploadedAt = b.UploadedAt.UTC()
	if len(files) > 0 {
		if err := json.Unmarshal(files, &b.Files); err != nil {
			return b, err
		}
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &b.Args); err != nil {
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
	b.Files = slices.Clone(b.Files)
	b.Args = slices.Clone(b.Args)
	b.Timeout = b.ExecutionTimeout()
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
	for i := range out {
		out[i].Files = slices.Clone(out[i].Files)
		out[i].Args = slices.Clone(out[i].Args)
	}
	slices.Reverse(out) // mais novo primeiro, como no Postgres
	return out, nil
}

func (m *Memory) ScriptBundle(_ context.Context, id int64) (ScriptBundle, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.bundles {
		if b.ID == id {
			b.Files = slices.Clone(b.Files)
			b.Args = slices.Clone(b.Args)
			return b, true, nil
		}
	}
	return ScriptBundle{}, false, nil
}

func (m *Memory) SetScriptExecution(_ context.Context, id int64, entry string, args []string, timeout int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.bundles {
		if m.bundles[i].ID == id {
			m.bundles[i].Entry = entry
			m.bundles[i].Args = slices.Clone(args)
			m.bundles[i].Timeout = timeout
			return true, nil
		}
	}
	return false, nil
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
