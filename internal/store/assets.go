package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Localização física dos equipamentos, do patrimônio (Aula 23.6). Cada importação
// substitui a anterior inteira, numa transação: quem lê vê o arquivo velho ou o novo, nunca
// metade.

// AssetLocation é um bem do patrimônio: o tombo (sem zeros à esquerda) e onde ele está.
type AssetLocation struct {
	Tombo, Building, Room, Unit, Description, Subgroup string
}

// AssetImport é uma importação: quem, quando, de que arquivo e as contagens.
type AssetImport struct {
	ID                        int64
	At                        time.Time
	By, File                  string
	Kept, Skipped, Duplicates int
}

// ErrNoAssets: a importação sem nenhum bem não substitui nada (um arquivo errado não apaga a
// localização da frota inteira).
var ErrNoAssets = errors.New("nenhum bem para importar")

// ReplaceAssetLocations troca toda a localização pela lista e registra a importação.
func (p *Postgres) ReplaceAssetLocations(ctx context.Context, list []AssetLocation, imp AssetImport) error {
	if len(list) == 0 {
		return ErrNoAssets
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM asset_locations`); err != nil {
		return err
	}
	rows := make([][]any, 0, len(list))
	for _, a := range list {
		rows = append(rows, []any{a.Tombo, a.Building, a.Room, a.Unit, a.Description, a.Subgroup})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"asset_locations"},
		[]string{"tombo", "building", "room", "unit", "description", "subgroup"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_imports (imported_by, file_name, rows_kept, rows_skipped, duplicates) VALUES ($1, $2, $3, $4, $5)`,
		imp.By, imp.File, len(list), imp.Skipped, imp.Duplicates); err != nil {
		return err
	}
	// O histórico guarda as 50 últimas.
	if _, err := tx.Exec(ctx, `DELETE FROM asset_imports WHERE id NOT IN (SELECT id FROM asset_imports ORDER BY id DESC LIMIT 50)`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AssetLocations: todos os bens, pelo tombo.
func (p *Postgres) AssetLocations(ctx context.Context) ([]AssetLocation, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT tombo, building, room, unit, description, subgroup FROM asset_locations ORDER BY length(tombo), tombo`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AssetLocation, error) {
		var a AssetLocation
		err := r.Scan(&a.Tombo, &a.Building, &a.Room, &a.Unit, &a.Description, &a.Subgroup)
		return a, err
	})
}

// AssetImports: as últimas importações, da mais nova.
func (p *Postgres) AssetImports(ctx context.Context, limit int) ([]AssetImport, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, imported_at, imported_by, file_name, rows_kept, rows_skipped, duplicates
		FROM asset_imports ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AssetImport, error) {
		var i AssetImport
		err := r.Scan(&i.ID, &i.At, &i.By, &i.File, &i.Kept, &i.Skipped, &i.Duplicates)
		i.At = i.At.UTC()
		return i, err
	})
}

// ReplaceAssetLocations (memória).
func (m *Memory) ReplaceAssetLocations(_ context.Context, list []AssetLocation, imp AssetImport) error {
	if len(list) == 0 {
		return ErrNoAssets
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locations = slices.Clone(list)
	m.assetSeq++
	imp.ID, imp.At, imp.Kept = m.assetSeq, m.now(), len(list)
	m.assetImports = append(m.assetImports, imp)
	if len(m.assetImports) > 50 {
		m.assetImports = m.assetImports[len(m.assetImports)-50:]
	}
	return nil
}

// AssetLocations (memória).
func (m *Memory) AssetLocations(context.Context) ([]AssetLocation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := slices.Clone(m.locations)
	slices.SortFunc(out, func(a, b AssetLocation) int {
		if len(a.Tombo) != len(b.Tombo) {
			return len(a.Tombo) - len(b.Tombo)
		}
		return strings.Compare(a.Tombo, b.Tombo)
	})
	if out == nil {
		out = []AssetLocation{}
	}
	return out, nil
}

// AssetImports (memória).
func (m *Memory) AssetImports(_ context.Context, limit int) ([]AssetImport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []AssetImport{}
	for i := len(m.assetImports) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.assetImports[i])
	}
	return out, nil
}
