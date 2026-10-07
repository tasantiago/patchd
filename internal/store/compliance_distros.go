package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
)

// ErrMachineAmbiguous: o prefixo casa com mais de uma máquina.
var ErrMachineAmbiguous = errors.New("o prefixo casa com mais de uma máquina")

// ResolveMachine devolve o ID completo da máquina a partir de um prefixo (ou do ID inteiro).
func (p *Postgres) ResolveMachine(ctx context.Context, prefix string) (string, bool, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM machines WHERE id LIKE $1 || '%' ORDER BY id LIMIT 2`, prefix)
	if err != nil {
		return "", false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", false, err
	}
	switch len(ids) {
	case 0:
		return "", false, nil
	case 1:
		return ids[0], true, nil
	default:
		return "", false, fmt.Errorf("%q: %w", prefix, ErrMachineAmbiguous)
	}
}

// UbuntuEcosystemsKnown lista os ecossistemas do Ubuntu presentes no catálogo.
func (p *Postgres) UbuntuEcosystemsKnown(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT ecosystem FROM ubuntu_usn_fixes ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// FedoraReleasesKnown lista as versões do Fedora presentes no catálogo.
func (p *Postgres) FedoraReleasesKnown(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT release FROM fedora_updates ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// UbuntuAdvisories devolve as correções das USNs válidas de um ecossistema para os pacotes
// fonte pedidos, com as CVEs e as que estão no KEV.
func (p *Postgres) UbuntuAdvisories(ctx context.Context, ecosystem string, sources []string) ([]compliance.Advisory, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT u.id, f.source_package, f.fixed_version, u.published,
		       coalesce((SELECT c.priority FROM ubuntu_usn_cves c WHERE c.usn_id = u.id AND c.ecosystem = f.ecosystem
		                  ORDER BY array_position(ARRAY['negligible','low','medium','high','critical'], c.priority) DESC NULLS LAST
		                  LIMIT 1), ''),
		       coalesce((SELECT array_agg(c.cve ORDER BY c.cve) FROM ubuntu_usn_cves c
		                  WHERE c.usn_id = u.id AND c.ecosystem = f.ecosystem), '{}'),
		       coalesce((SELECT array_agg(c.cve ORDER BY c.cve) FROM ubuntu_usn_cves c JOIN kev_vulns k ON k.cve = c.cve
		                  WHERE c.usn_id = u.id AND c.ecosystem = f.ecosystem), '{}')
		FROM ubuntu_usn_fixes f JOIN ubuntu_usns u ON u.id = f.usn_id
		WHERE f.ecosystem = $1 AND f.source_package = ANY($2) AND u.withdrawn IS NULL`, ecosystem, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.Advisory
	for rows.Next() {
		var a compliance.Advisory
		if err := rows.Scan(&a.ID, &a.Source, &a.Fixed, &a.Published, &a.Severity, &a.CVEs, &a.KEV); err != nil {
			return nil, err
		}
		a.Published = a.Published.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// FedoraAdvisories devolve os builds dos updates de segurança de uma versão do Fedora para
// os pacotes fonte pedidos. A versão da correção vem como época:versão-release, e a data é
// a de entrada em stable (ou a de publicação, se o Bodhi não informar).
func (p *Postgres) FedoraAdvisories(ctx context.Context, release string, sources []string) ([]compliance.Advisory, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT u.alias, b.package, b.epoch || ':' || b.version || '-' || b.rpm_release,
		       coalesce(u.date_stable, u.date_pushed), u.severity,
		       coalesce((SELECT array_agg(c.cve ORDER BY c.cve) FROM fedora_update_cves c WHERE c.alias = u.alias), '{}'),
		       coalesce((SELECT array_agg(c.cve ORDER BY c.cve) FROM fedora_update_cves c JOIN kev_vulns k ON k.cve = c.cve
		                  WHERE c.alias = u.alias), '{}'),
		       u.cves_partial
		FROM fedora_update_builds b JOIN fedora_updates u ON u.alias = b.alias
		WHERE u.release = $1 AND b.package = ANY($2)`, release, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.Advisory
	for rows.Next() {
		var a compliance.Advisory
		var when *time.Time
		if err := rows.Scan(&a.ID, &a.Source, &a.Fixed, &when, &a.Severity, &a.CVEs, &a.KEV, &a.Partial); err != nil {
			return nil, err
		}
		if when != nil {
			a.Published = when.UTC()
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// KEVEntries devolve o catálogo KEV guardado (ID, fabricante, produto e inclusão), para a
// inferência no Fedora.
func (p *Postgres) KEVEntries(ctx context.Context) ([]kev.Vuln, error) {
	rows, err := p.pool.Query(ctx, `SELECT cve, vendor, product, date_added FROM kev_vulns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kev.Vuln
	for rows.Next() {
		var v kev.Vuln
		if err := rows.Scan(&v.CVE, &v.Vendor, &v.Product, &v.Added); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
