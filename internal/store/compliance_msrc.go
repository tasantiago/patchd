package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/compliance"
)

// WindowsProducts lista os produtos do MSRC de Windows cliente (10 e 11).
func (p *Postgres) WindowsProducts(ctx context.Context) ([]compliance.MSRCProduct, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, name FROM msrc_products
		WHERE name ILIKE 'Windows 10 %' OR name ILIKE 'Windows 11 %'
		ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (compliance.MSRCProduct, error) {
		var pr compliance.MSRCProduct
		err := r.Scan(&pr.ID, &pr.Name)
		return pr, err
	})
}

// WindowsFixes devolve, de todos os documentos, as CVEs ligadas aos produtos pedidos e as
// correções delas para esses produtos. Uma CVE entra se o produto está entre os afetados
// (msrc_affected) ou entre os corrigidos (msrc_fixes): é o produto que separa a CVE do
// Windows da CVE do Azure Linux ou do Edge no mesmo documento. Uma linha sem KB é uma CVE
// que, naquele documento, não tem correção com KB para o produto.
func (p *Postgres) WindowsFixes(ctx context.Context, productIDs []string) ([]compliance.WindowsFix, error) {
	rows, err := p.pool.Query(ctx, `
		WITH alvo AS (
			SELECT document_id, cve FROM msrc_affected WHERE product_id = ANY($1)
			UNION
			SELECT document_id, cve FROM msrc_fixes WHERE product_id = ANY($1)
		)
		SELECT x.document_id, d.initial_release, x.cve, v.title, v.exploited,
		       EXISTS (SELECT 1 FROM kev_vulns k WHERE k.cve = x.cve),
		       coalesce(a.severity, ''), coalesce(a.base_score, 0)::float8,
		       coalesce(f.kb, ''), coalesce(f.subtype, ''), coalesce(f.fixed_build, '')
		FROM alvo x
		JOIN msrc_documents d ON d.id = x.document_id
		JOIN msrc_vulns v ON v.document_id = x.document_id AND v.cve = x.cve
		LEFT JOIN LATERAL (
			SELECT a.severity, a.base_score FROM msrc_affected a
			WHERE a.document_id = x.document_id AND a.cve = x.cve AND a.product_id = ANY($1)
			ORDER BY a.base_score DESC LIMIT 1) a ON true
		LEFT JOIN msrc_fixes f ON f.document_id = x.document_id AND f.cve = x.cve AND f.product_id = ANY($1)
		ORDER BY x.cve, d.initial_release, f.kb`, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.WindowsFix
	for rows.Next() {
		var f compliance.WindowsFix
		var released time.Time
		if err := rows.Scan(&f.Document, &released, &f.CVE, &f.Title, &f.Exploited, &f.KEV,
			&f.Severity, &f.Score, &f.KB, &f.SubType, &f.Build); err != nil {
			return nil, err
		}
		f.Released = released.UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}
