package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/osv"
)

// UbuntuVersions devolve, para cada USN guardada, a data do modified_id.csv no momento em
// que foi baixada, como texto.
func (p *Postgres) UbuntuVersions(ctx context.Context) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, modified FROM ubuntu_usns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// SaveUbuntuUSN grava uma USN inteira numa transação, substituindo a versão anterior.
// modified é a data da lista (modified_id.csv), a que a próxima sincronização compara.
// Uma USN retirada (r.Withdrawn) fica guardada sem correções nem CVEs: a linha é o que
// impede a sincronização de baixá-la de novo a cada execução.
func (p *Postgres) SaveUbuntuUSN(ctx context.Context, r osv.Record, modified string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM ubuntu_usns WHERE id = $1`, r.ID); err != nil {
		return err
	}
	var withdrawn *time.Time
	if !r.Withdrawn.IsZero() {
		withdrawn = &r.Withdrawn
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ubuntu_usns (id, summary, published, modified, withdrawn) VALUES ($1, $2, $3, $4, $5)`,
		r.ID, r.Summary, r.Published, modified, withdrawn); err != nil {
		return err
	}
	if withdrawn != nil {
		return tx.Commit(ctx)
	}

	fixes := [][]any{}
	seenF := map[osv.Fix]bool{}
	for _, f := range r.Fixes {
		if seenF[f] {
			continue
		}
		seenF[f] = true
		fixes = append(fixes, []any{r.ID, f.Ecosystem, f.Package, f.Version})
	}
	cves := [][]any{}
	seenC := map[[2]string]bool{}
	for _, c := range r.CVEs {
		k := [2]string{c.Ecosystem, c.ID}
		if seenC[k] {
			continue
		}
		seenC[k] = true
		cves = append(cves, []any{r.ID, c.Ecosystem, c.ID, c.Priority, c.CVSS})
	}

	copies := []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"ubuntu_usn_fixes", []string{"usn_id", "ecosystem", "source_package", "fixed_version"}, fixes},
		{"ubuntu_usn_cves", []string{"usn_id", "ecosystem", "cve", "priority", "cvss"}, cves},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("%s: %w", c.table, err)
		}
	}
	return tx.Commit(ctx)
}

// UbuntuFix é uma linha do "catalog usn": num ecossistema, a versão que corrige o pacote.
type UbuntuFix struct {
	USN         string
	Published   time.Time
	Ecosystem   string
	Version     string
	CVEs        int
	TopPriority string // a maior prioridade entre as CVEs do aviso naquele ecossistema
	Summary     string
}

// UbuntuFixes lista as correções de um pacote fonte, da USN mais nova para a mais velha.
// release filtra pela versão do Ubuntu sem o Pro ("26.04" pega Ubuntu:26.04:LTS); vazio
// lista todos os ecossistemas, Pro incluído.
func (p *Postgres) UbuntuFixes(ctx context.Context, pkg, release string) ([]UbuntuFix, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT u.id, u.published, f.ecosystem, f.fixed_version, u.summary,
		       (SELECT count(*) FROM ubuntu_usn_cves c WHERE c.usn_id = u.id AND c.ecosystem = f.ecosystem),
		       coalesce((SELECT c.priority FROM ubuntu_usn_cves c
		                  WHERE c.usn_id = u.id AND c.ecosystem = f.ecosystem
		                  ORDER BY array_position(ARRAY['negligible','low','medium','high','critical'], c.priority) DESC NULLS LAST
		                  LIMIT 1), '')
		FROM ubuntu_usn_fixes f JOIN ubuntu_usns u ON u.id = f.usn_id
		WHERE f.source_package = $1 AND u.withdrawn IS NULL
		  AND ($2 = '' OR f.ecosystem = 'Ubuntu:' || $2 OR f.ecosystem LIKE 'Ubuntu:' || $2 || ':%')
		ORDER BY u.published DESC, f.ecosystem`, pkg, release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UbuntuFix
	for rows.Next() {
		var f UbuntuFix
		if err := rows.Scan(&f.USN, &f.Published, &f.Ecosystem, &f.Version, &f.Summary, &f.CVEs, &f.TopPriority); err != nil {
			return nil, err
		}
		f.Published = f.Published.UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// UbuntuCounts resume o catálogo do Ubuntu para o "catalog list".
type UbuntuCounts struct {
	USNs, Withdrawn, Fixes, CVEs int
	Newest                       time.Time // publicação da USN válida mais nova
}

// UbuntuSummary conta o que está guardado; as USNs retiradas são contadas à parte.
func (p *Postgres) UbuntuSummary(ctx context.Context) (UbuntuCounts, error) {
	var c UbuntuCounts
	var newest *time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM ubuntu_usns WHERE withdrawn IS NULL),
		       (SELECT count(*) FROM ubuntu_usns WHERE withdrawn IS NOT NULL),
		       (SELECT count(*) FROM ubuntu_usn_fixes),
		       (SELECT count(DISTINCT cve) FROM ubuntu_usn_cves),
		       (SELECT max(published) FROM ubuntu_usns WHERE withdrawn IS NULL)`).Scan(&c.USNs, &c.Withdrawn, &c.Fixes, &c.CVEs, &newest)
	if newest != nil {
		c.Newest = newest.UTC()
	}
	return c, err
}
