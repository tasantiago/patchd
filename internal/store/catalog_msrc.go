package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

// MSRCVersions devolve, para cada documento do MSRC guardado, a data de revisão da lista
// no momento em que foi baixado.
func (p *Postgres) MSRCVersions(ctx context.Context) (map[string]time.Time, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, current_release FROM msrc_documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t.UTC()
	}
	return out, rows.Err()
}

// SaveMSRCDocument grava um documento inteiro numa transação, substituindo a versão
// anterior dele. current é a data de revisão da lista (/updates), a que a próxima
// sincronização compara. As linhas vão por COPY: um mês tem dezenas de milhares.
func (p *Postgres) SaveMSRCDocument(ctx context.Context, d msrc.Document, current time.Time) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM msrc_documents WHERE id = $1`, d.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO msrc_documents (id, title, initial_release, current_release, skipped_fixes)
		VALUES ($1, $2, $3, $4, $5)`, d.ID, d.Title, d.Initial, current, d.SkippedFix); err != nil {
		return err
	}

	for _, pr := range d.Products {
		if _, err := tx.Exec(ctx, `
			INSERT INTO msrc_products (id, name) VALUES ($1, $2)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name`, pr.ID, pr.Name); err != nil {
			return err
		}
	}

	// Repetições dentro do documento (a mesma CVE, ou a mesma correção listada duas vezes)
	// ficam com a primeira ocorrência: a chave primária não aceita duplicatas.
	vulns := [][]any{}
	seenV := map[string]bool{}
	for _, v := range d.Vulns {
		if seenV[v.CVE] {
			continue
		}
		seenV[v.CVE] = true
		vulns = append(vulns, []any{d.ID, v.CVE, v.Title, v.Exploited, v.Disclosed, v.Exploit})
	}
	affected := [][]any{}
	seenA := map[[2]string]bool{}
	for _, a := range d.Affected {
		k := [2]string{a.CVE, a.ProductID}
		if seenA[k] || !seenV[a.CVE] {
			continue
		}
		seenA[k] = true
		affected = append(affected, []any{d.ID, a.CVE, a.ProductID, a.Severity, a.Impact, a.BaseScore, a.Vector})
	}
	fixes := [][]any{}
	seenF := map[[4]string]bool{}
	for _, f := range d.Fixes {
		k := [4]string{f.CVE, f.ProductID, f.KB, f.FixedBuild}
		if seenF[k] || !seenV[f.CVE] {
			continue
		}
		seenF[k] = true
		fixes = append(fixes, []any{d.ID, f.CVE, f.ProductID, f.KB, f.SubType, f.FixedBuild, f.Supersedes, f.RestartRequired})
	}

	copies := []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"msrc_vulns", []string{"document_id", "cve", "title", "exploited", "disclosed", "exploit"}, vulns},
		{"msrc_affected", []string{"document_id", "cve", "product_id", "severity", "impact", "base_score", "vector"}, affected},
		{"msrc_fixes", []string{"document_id", "cve", "product_id", "kb", "subtype", "fixed_build", "supersedes", "restart_required"}, fixes},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("%s: %w", c.table, err)
		}
	}
	return tx.Commit(ctx)
}

// MSRCDocumentSummary é uma linha do "catalog list".
type MSRCDocumentSummary struct {
	ID        string
	Title     string
	Current   time.Time
	FetchedAt time.Time
	Vulns     int
	Exploited int
	Fixes     int
}

// MSRCDocuments resume os documentos guardados, do mais novo para o mais velho.
func (p *Postgres) MSRCDocuments(ctx context.Context) ([]MSRCDocumentSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT d.id, d.title, d.current_release, d.fetched_at,
		       (SELECT count(*) FROM msrc_vulns v WHERE v.document_id = d.id),
		       (SELECT count(*) FROM msrc_vulns v WHERE v.document_id = d.id AND v.exploited),
		       (SELECT count(*) FROM msrc_fixes f WHERE f.document_id = d.id)
		FROM msrc_documents d
		ORDER BY d.initial_release DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MSRCDocumentSummary
	for rows.Next() {
		var s MSRCDocumentSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Current, &s.FetchedAt, &s.Vulns, &s.Exploited, &s.Fixes); err != nil {
			return nil, err
		}
		s.Current, s.FetchedAt = s.Current.UTC(), s.FetchedAt.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// MSRCBuild é o maior build corrigido de um produto num documento.
type MSRCBuild struct {
	Product  string
	Document string
	Build    string
	KBs      string // os KBs que levam a esse build, separados por vírgula
}

// MSRCBuilds lista, para os produtos cujo nome começa com prefix (sem diferenciar maiúsculas:
// o próprio MSRC escreve "Version" e "version"), o maior build corrigido em cada documento. A comparação é numérica, parte a parte (10.0.26200.9445 como
// {10,0,26200,9445}), nunca como texto.
func (p *Postgres) MSRCBuilds(ctx context.Context, prefix string) ([]MSRCBuild, error) {
	rows, err := p.pool.Query(ctx, `
		WITH b AS (
			SELECT pr.name, f.document_id, f.kb, string_to_array(f.fixed_build, '.')::int[] AS v
			FROM msrc_fixes f JOIN msrc_products pr ON pr.id = f.product_id
			WHERE f.fixed_build ~ '^[0-9]+(\.[0-9]+)+$' AND pr.name ILIKE $1 || '%'
		), m AS (
			SELECT name, document_id, max(v) AS v FROM b GROUP BY name, document_id
		)
		SELECT m.name, m.document_id, array_to_string(m.v, '.'),
		       (SELECT string_agg(DISTINCT b.kb, ',' ORDER BY b.kb) FROM b
		         WHERE b.name = m.name AND b.document_id = m.document_id AND b.v = m.v)
		FROM m JOIN msrc_documents d ON d.id = m.document_id
		ORDER BY m.name, d.initial_release DESC`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MSRCBuild
	for rows.Next() {
		var b MSRCBuild
		if err := rows.Scan(&b.Product, &b.Document, &b.Build, &b.KBs); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
