package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/kev"
)

// FeedKEV é a linha do KEV em catalog_feeds.
const FeedKEV = "cisa-kev"

// SaveKEV substitui o catálogo KEV, e o dateReleased junto, numa transação.
func (p *Postgres) SaveKEV(ctx context.Context, c kev.Catalog) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM kev_vulns`); err != nil {
		return err
	}
	rows := make([][]any, 0, len(c.Vulns))
	for _, v := range c.Vulns {
		rows = append(rows, []any{v.CVE, v.Vendor, v.Product, v.Name, v.Added, nullDay(v.Due), v.Ransomware, v.CWEs})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"kev_vulns"},
		[]string{"cve", "vendor", "product", "name", "date_added", "due_date", "ransomware", "cwes"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("kev_vulns: %w", err)
	}
	if err := saveFeedHash(ctx, tx, FeedKEV, c.Released); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// KEVCoverage é uma linha do "catalog kev": quanto de uma fonte o KEV cobre.
type KEVCoverage struct {
	Source    string
	CVEs      int // CVEs distintas da fonte
	InKEV     int // delas, as que estão no KEV
	Vendor    int // marcadas como exploradas pelo próprio fabricante (-1: a fonte não informa)
	Exploited int // no KEV ou marcadas pelo fabricante: o que a priorização considera explorado
}

// KEVCoverage compara cada fonte do catálogo com o KEV.
func (p *Postgres) KEVCoverage(ctx context.Context) ([]KEVCoverage, error) {
	rows, err := p.pool.Query(ctx, `
		WITH f AS (
			SELECT 'MSRC' AS fonte, cve, bool_or(exploited) AS fab FROM msrc_vulns GROUP BY cve
			UNION ALL
			SELECT 'Ubuntu', c.cve, NULL FROM ubuntu_usn_cves c JOIN ubuntu_usns u ON u.id = c.usn_id
			 WHERE u.withdrawn IS NULL GROUP BY c.cve
			UNION ALL
			SELECT 'Fedora', cve, NULL FROM fedora_update_cves GROUP BY cve
			UNION ALL
			SELECT 'Apple', cve, bool_or(exploited) FROM apple_release_cves GROUP BY cve
		)
		SELECT f.fonte, count(*), count(k.cve),
		       CASE WHEN bool_and(f.fab IS NULL) THEN -1 ELSE count(*) FILTER (WHERE f.fab) END,
		       count(*) FILTER (WHERE k.cve IS NOT NULL OR f.fab)
		FROM f LEFT JOIN kev_vulns k ON k.cve = f.cve
		GROUP BY f.fonte
		ORDER BY array_position(ARRAY['MSRC','Ubuntu','Fedora','Apple'], f.fonte)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KEVCoverage
	for rows.Next() {
		var c KEVCoverage
		if err := rows.Scan(&c.Source, &c.CVEs, &c.InKEV, &c.Vendor, &c.Exploited); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// KEVRecentVuln é uma CVE incluída no KEV recentemente, com onde ela aparece no catálogo.
type KEVRecentVuln struct {
	kev.Vuln
	MSRC   []string // documentos do MSRC que a citam
	USNs   int      // USNs válidas que a citam
	Fedora []string // versões do Fedora com update que a cita diretamente
	Apple  []string // versões do macOS que a corrigem
	// Inferred: sem citação direta no Fedora, o primeiro update de segurança estável de cada
	// pacote mapeado (kev.FedoraRules), em cada versão, a partir da inclusão no KEV.
	Inferred []FedoraInferred
}

// FedoraInferred é um update do Fedora que provavelmente corrige uma CVE do KEV.
type FedoraInferred struct {
	Release string
	Alias   string
	NVR     string
}

// KEVRecent lista as CVEs incluídas no KEV desde since, da mais nova para a mais antiga,
// com onde cada uma aparece no catálogo.
func (p *Postgres) KEVRecent(ctx context.Context, since time.Time) ([]KEVRecentVuln, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT k.cve, k.vendor, k.product, k.name, k.date_added, k.due_date, k.ransomware, k.cwes,
		       coalesce((SELECT array_agg(DISTINCT v.document_id) FROM msrc_vulns v WHERE v.cve = k.cve), '{}'),
		       (SELECT count(DISTINCT c.usn_id) FROM ubuntu_usn_cves c JOIN ubuntu_usns u ON u.id = c.usn_id
		         WHERE c.cve = k.cve AND u.withdrawn IS NULL),
		       coalesce((SELECT array_agg(DISTINCT u.release) FROM fedora_update_cves c JOIN fedora_updates u ON u.alias = c.alias
		         WHERE c.cve = k.cve), '{}'),
		       coalesce((SELECT array_agg(a.product_version ORDER BY a.product_version) FROM apple_release_cves a
		         WHERE a.cve = k.cve), '{}')
		FROM kev_vulns k
		WHERE k.date_added >= $1
		ORDER BY k.date_added DESC, k.cve`, since)
	if err != nil {
		return nil, err
	}
	var out []KEVRecentVuln
	for rows.Next() {
		var r KEVRecentVuln
		var due *time.Time
		if err := rows.Scan(&r.CVE, &r.Vendor, &r.Product, &r.Name, &r.Added, &due, &r.Ransomware, &r.CWEs,
			&r.MSRC, &r.USNs, &r.Fedora, &r.Apple); err != nil {
			rows.Close()
			return nil, err
		}
		if due != nil {
			r.Due = *due
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Inferência no Fedora, só onde não há citação direta.
	for i := range out {
		if len(out[i].Fedora) > 0 {
			continue
		}
		for _, pkg := range kev.FedoraPackages(out[i].Vendor, out[i].Product) {
			inf, err := p.fedoraFirstSince(ctx, pkg, out[i].Added)
			if err != nil {
				return nil, err
			}
			out[i].Inferred = append(out[i].Inferred, inf...)
		}
	}
	return out, nil
}

// fedoraFirstSince devolve, por versão do Fedora, o primeiro update de segurança estável
// do pacote a partir de since.
func (p *Postgres) fedoraFirstSince(ctx context.Context, pkg string, since time.Time) ([]FedoraInferred, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT ON (u.release) u.release, u.alias, b.nvr
		FROM fedora_update_builds b JOIN fedora_updates u ON u.alias = b.alias
		WHERE b.package = $1 AND u.date_stable >= $2
		ORDER BY u.release, u.date_stable, u.alias`, pkg, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FedoraInferred
	for rows.Next() {
		var f FedoraInferred
		if err := rows.Scan(&f.Release, &f.Alias, &f.NVR); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// KEVSummary resume o catálogo KEV guardado.
type KEVSummary struct {
	Released   string
	Total      int
	Ransomware int
	Last30     int // incluídas nos últimos 30 dias
}

// KEVInfo devolve o resumo; Released vazio quando o KEV nunca foi sincronizado.
func (p *Postgres) KEVInfo(ctx context.Context) (KEVSummary, error) {
	var s KEVSummary
	var err error
	if s.Released, err = p.FeedHash(ctx, FeedKEV); err != nil {
		return s, err
	}
	err = p.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE ransomware),
		       count(*) FILTER (WHERE date_added >= current_date - 30)
		FROM kev_vulns`).Scan(&s.Total, &s.Ransomware, &s.Last30)
	return s, err
}
