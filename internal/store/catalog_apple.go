package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/apple"
)

// Fontes da Apple em catalog_feeds.
const (
	FeedAppleGDMF = "apple-gdmf"
	FeedAppleSOFA = "apple-sofa"
)

// FeedHash devolve o hash guardado de uma fonte; vazio quando ela nunca foi gravada.
func (p *Postgres) FeedHash(ctx context.Context, source string) (string, error) {
	var h string
	err := p.pool.QueryRow(ctx, `SELECT hash FROM catalog_feeds WHERE source = $1`, source).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func saveFeedHash(ctx context.Context, tx pgx.Tx, source, hash string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO catalog_feeds (source, hash) VALUES ($1, $2)
		ON CONFLICT (source) DO UPDATE SET hash = EXCLUDED.hash, fetched_at = now()`, source, hash)
	return err
}

func nullDay(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// SaveAppleVersions substitui as versões do gdmf, e o hash junto, numa transação.
func (p *Postgres) SaveAppleVersions(ctx context.Context, vs []apple.GDMFVersion, hash string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM apple_versions`); err != nil {
		return err
	}
	rows := [][]any{}
	seen := map[[2]string]bool{}
	for _, v := range vs {
		k := [2]string{v.ProductVersion, v.Build}
		if seen[k] {
			continue
		}
		seen[k] = true
		rows = append(rows, []any{v.ProductVersion, v.Build, v.Extra, v.PrerequisiteBuild, nullDay(v.Posting), nullDay(v.Expiration), v.Devices})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"apple_versions"},
		[]string{"product_version", "build", "extra", "prerequisite_build", "posting_date", "expiration_date", "devices"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("apple_versions: %w", err)
	}
	if err := saveFeedHash(ctx, tx, FeedAppleGDMF, hash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SaveSOFA substitui as versões de segurança, as CVEs e os modelos, e o UpdateHash junto,
// numa transação.
func (p *Postgres) SaveSOFA(ctx context.Context, f apple.SOFAFeed) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM apple_releases`); err != nil {
		return err
	}
	rels, cves := [][]any{}, [][]any{}
	for _, r := range f.Releases {
		builds := r.Builds
		if builds == nil {
			builds = []string{}
		}
		rels = append(rels, []any{r.ProductVersion, r.Major, r.MajorNumber, r.UpdateName, builds, r.Date, r.SecurityInfo, r.UniqueCVEs})
		for _, c := range r.CVEs {
			cves = append(cves, []any{r.ProductVersion, c.ID, c.Exploited, c.InKEV, c.Severity})
		}
	}
	copies := []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"apple_releases", []string{"product_version", "major", "major_number", "update_name", "builds", "release_date", "security_info", "unique_cves"}, rels},
		{"apple_release_cves", []string{"product_version", "cve", "exploited", "in_kev", "severity"}, cves},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("%s: %w", c.table, err)
		}
	}
	// Feed sem a seção Models (formato antigo, ou recorte): os modelos guardados ficam.
	if len(f.Models) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM apple_models`); err != nil {
			return err
		}
		models := make([][]any, 0, len(f.Models))
		for _, m := range f.Models {
			majors := m.Majors
			if majors == nil {
				majors = []int{}
			}
			models = append(models, []any{m.ID, m.MarketingName, majors})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"apple_models"}, []string{"model", "marketing_name", "majors"},
			pgx.CopyFromRows(models)); err != nil {
			return fmt.Errorf("apple_models: %w", err)
		}
	}
	if err := saveFeedHash(ctx, tx, FeedAppleSOFA, f.UpdateHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AppleMajor é uma linha do "catalog apple": a última versão de segurança de uma major,
// lado a lado com a última versão normal que o gdmf publica para ela.
type AppleMajor struct {
	Major       string
	Number      string
	Latest      string // a versão de segurança mais recente (SOFA)
	Builds      []string
	Date        time.Time
	Exploited   int    // CVEs exploradas corrigidas na versão mais recente
	GDMFLatest  string // a maior versão publicada pelo gdmf para a major (sem as "(a)"); vazio se nenhuma
	GDMFBuilds  []string
	SecurityURL string
}

// AppleMajors resume cada major, da mais nova para a mais antiga.
func (p *Postgres) AppleMajors(ctx context.Context) ([]AppleMajor, error) {
	rows, err := p.pool.Query(ctx, `
		WITH ult AS (
			SELECT DISTINCT ON (major_number) *
			FROM apple_releases
			ORDER BY major_number, release_date DESC, product_version DESC
		), g AS (
			SELECT split_part(product_version, '.', 1) AS major_number, product_version,
			       array_agg(build ORDER BY build) AS builds
			FROM apple_versions
			WHERE extra = '' AND product_version ~ '^[0-9]+(\.[0-9]+)*$'
			GROUP BY 1, 2
		), gmax AS (
			SELECT DISTINCT ON (major_number) major_number, product_version, builds
			FROM g ORDER BY major_number, string_to_array(product_version, '.')::int[] DESC
		)
		SELECT u.major, u.major_number, u.product_version, u.builds, u.release_date, u.security_info,
		       (SELECT count(*) FROM apple_release_cves c WHERE c.product_version = u.product_version AND c.exploited),
		       coalesce(gmax.product_version, ''), coalesce(gmax.builds, '{}')
		FROM ult u LEFT JOIN gmax ON gmax.major_number = u.major_number
		ORDER BY u.major_number::int DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppleMajor
	for rows.Next() {
		var m AppleMajor
		if err := rows.Scan(&m.Major, &m.Number, &m.Latest, &m.Builds, &m.Date, &m.SecurityURL, &m.Exploited, &m.GDMFLatest, &m.GDMFBuilds); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppleRelease é uma linha do "catalog apple -release N".
type AppleRelease struct {
	Version   string
	Builds    []string
	Date      time.Time
	CVEs      int // a contagem do SOFA
	Exploited int
	URL       string
}

// AppleReleases lista as versões de segurança de uma major, da mais nova para a mais antiga.
func (p *Postgres) AppleReleases(ctx context.Context, major string) ([]AppleRelease, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT r.product_version, r.builds, r.release_date, r.unique_cves,
		       (SELECT count(*) FROM apple_release_cves c WHERE c.product_version = r.product_version AND c.exploited),
		       r.security_info
		FROM apple_releases r WHERE r.major_number = $1
		ORDER BY r.release_date DESC, r.product_version DESC`, major)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppleRelease
	for rows.Next() {
		var r AppleRelease
		if err := rows.Scan(&r.Version, &r.Builds, &r.Date, &r.CVEs, &r.Exploited, &r.URL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
