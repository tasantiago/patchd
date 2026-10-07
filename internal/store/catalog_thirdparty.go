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

// ThirdPartyVersions devolve as versões de referência guardadas.
func (p *Postgres) ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error) {
	rows, err := p.pool.Query(ctx, `SELECT app, os, version, source, fetched_at FROM thirdparty_versions ORDER BY os, app`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []thirdparty.Ref
	for rows.Next() {
		var r thirdparty.Ref
		if err := rows.Scan(&r.App, &r.OS, &r.Version, &r.Source, &r.Fetched); err != nil {
			return nil, err
		}
		r.Fetched = r.Fetched.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
