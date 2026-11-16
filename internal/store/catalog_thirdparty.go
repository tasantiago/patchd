package store

import (
	"context"

	"github.com/tasantiago/patchd/internal/thirdparty"
)

// SaveThirdPartyVersion grava (ou atualiza) a versão de referência de uma regra.
func (p *Postgres) SaveThirdPartyVersion(ctx context.Context, app, osID, version, source string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO thirdparty_versions (app, os, version, source) VALUES ($1, $2, $3, $4)
		ON CONFLICT (app, os) DO UPDATE SET version = EXCLUDED.version, source = EXCLUDED.source, fetched_at = now()`,
		app, osID, version, source)
	return err
}

// PruneThirdPartyVersions apaga as versões de regras que saíram do manifesto (ou deixaram de
// ter fonte). keep são as chaves "app/os" que ficam.
func (p *Postgres) PruneThirdPartyVersions(ctx context.Context, keep []string) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM thirdparty_versions WHERE NOT (app || '/' || os = ANY($1))`, keep)
	return tag.RowsAffected(), err
}

// SaveThirdPartyCheck grava a versão da segunda fonte de uma regra (a linha já existe: a
// fonte principal grava primeiro). version vazia apaga a conferência.
func (p *Postgres) SaveThirdPartyCheck(ctx context.Context, app, osID, version, source string) error {
	_, err := p.pool.Exec(ctx, `UPDATE thirdparty_versions SET check_version = $3, check_source = $4 WHERE app = $1 AND os = $2`,
		app, osID, version, source)
	return err
}

// ReplaceLifecycle troca os ciclos guardados de um produto pelos novos, numa transação.
func (p *Postgres) ReplaceLifecycle(ctx context.Context, product string, cycles []thirdparty.Cycle) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit
	if _, err := tx.Exec(ctx, `DELETE FROM thirdparty_lifecycles WHERE product = $1`, product); err != nil {
		return err
	}
	for _, c := range cycles {
		if _, err := tx.Exec(ctx, `INSERT INTO thirdparty_lifecycles (product, cycle, eol_from, is_eol, latest) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (product, cycle) DO NOTHING`, product, c.Name, c.EOLFrom, c.IsEOL, c.Latest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PruneLifecycles apaga os ciclos dos produtos que saíram do manifesto.
func (p *Postgres) PruneLifecycles(ctx context.Context, keep []string) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM thirdparty_lifecycles WHERE NOT (product = ANY($1))`, keep)
	return tag.RowsAffected(), err
}

// Lifecycles devolve os ciclos de suporte guardados.
func (p *Postgres) Lifecycles(ctx context.Context) ([]thirdparty.Cycle, error) {
	rows, err := p.pool.Query(ctx, `SELECT product, cycle, eol_from, is_eol, latest, fetched_at FROM thirdparty_lifecycles ORDER BY product, cycle`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []thirdparty.Cycle
	for rows.Next() {
		var c thirdparty.Cycle
		if err := rows.Scan(&c.Product, &c.Name, &c.EOLFrom, &c.IsEOL, &c.Latest, &c.Fetched); err != nil {
			return nil, err
		}
		if c.EOLFrom != nil {
			d := c.EOLFrom.UTC()
			c.EOLFrom = &d
		}
		c.Fetched = c.Fetched.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ThirdPartyVersions devolve as versões de referência guardadas.
func (p *Postgres) ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error) {
	rows, err := p.pool.Query(ctx, `SELECT app, os, version, source, fetched_at, check_version, check_source FROM thirdparty_versions ORDER BY os, app`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []thirdparty.Ref
	for rows.Next() {
		var r thirdparty.Ref
		if err := rows.Scan(&r.App, &r.OS, &r.Version, &r.Source, &r.Fetched, &r.CheckVersion, &r.CheckSource); err != nil {
			return nil, err
		}
		r.Fetched = r.Fetched.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
