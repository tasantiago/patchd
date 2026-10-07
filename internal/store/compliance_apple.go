package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/compliance"
)

// MacOSMajors resume cada major do SOFA: o lançamento (o x.0; sem ele, a versão mais
// antiga do catálogo) e a versão de segurança mais recente.
func (p *Postgres) MacOSMajors(ctx context.Context) ([]compliance.MacOSMajor, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT major_number, major,
		       coalesce(min(release_date) FILTER (WHERE product_version IN (major_number, major_number || '.0')), min(release_date)),
		       (array_agg(product_version ORDER BY release_date DESC, string_to_array(product_version, '.')::int[] DESC))[1],
		       max(release_date)
		FROM apple_releases
		WHERE major_number ~ '^[0-9]+$' AND product_version ~ '^[0-9]+(\.[0-9]+)*$'
		GROUP BY major_number, major
		ORDER BY major_number::int DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.MacOSMajor
	for rows.Next() {
		var m compliance.MacOSMajor
		var num string
		if err := rows.Scan(&num, &m.Name, &m.Launched, &m.Latest, &m.LatestDate); err != nil {
			return nil, err
		}
		if m.Number, err = strconv.Atoi(num); err != nil {
			return nil, err
		}
		m.Launched, m.LatestDate = m.Launched.UTC(), m.LatestDate.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// MacOSFixes devolve as CVEs corrigidas pelas versões de segurança de uma major. "KEV" vale
// a marca do SOFA ou a presença no KEV guardado (que pode ser mais recente que o SOFA).
func (p *Postgres) MacOSFixes(ctx context.Context, major int) ([]compliance.MacOSFix, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT r.product_version, r.release_date, c.cve, c.exploited,
		       c.in_kev OR EXISTS (SELECT 1 FROM kev_vulns k WHERE k.cve = c.cve), c.severity
		FROM apple_release_cves c JOIN apple_releases r USING (product_version)
		WHERE r.major_number = $1
		ORDER BY r.release_date, c.cve`, strconv.Itoa(major))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.MacOSFix
	for rows.Next() {
		var f compliance.MacOSFix
		var when time.Time
		if err := rows.Scan(&f.Version, &when, &f.CVE, &f.Exploited, &f.KEV, &f.Severity); err != nil {
			return nil, err
		}
		f.Released = when.UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// MacOSUnfixed devolve as CVEs exploradas (pela Apple ou no KEV) corrigidas em outras majors
// depois de "since" e nunca corrigidas na major pedida: o que uma major encerrada não vai
// receber.
func (p *Postgres) MacOSUnfixed(ctx context.Context, major int, since time.Time) ([]compliance.MacOSCVE, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT c.cve, bool_or(c.exploited), bool_or(c.in_kev OR EXISTS (SELECT 1 FROM kev_vulns k WHERE k.cve = c.cve)),
		       min(r.release_date),
		       array_agg(DISTINCT r.product_version ORDER BY r.product_version DESC)
		FROM apple_release_cves c JOIN apple_releases r USING (product_version)
		WHERE r.major_number <> $1 AND r.release_date > $2
		  AND (c.exploited OR c.in_kev OR EXISTS (SELECT 1 FROM kev_vulns k WHERE k.cve = c.cve))
		  AND NOT EXISTS (SELECT 1 FROM apple_release_cves c2 JOIN apple_releases r2 USING (product_version)
		                  WHERE r2.major_number = $1 AND c2.cve = c.cve)
		GROUP BY c.cve
		ORDER BY min(r.release_date) DESC, c.cve`, strconv.Itoa(major), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compliance.MacOSCVE
	for rows.Next() {
		var c compliance.MacOSCVE
		var when time.Time
		if err := rows.Scan(&c.CVE, &c.Exploited, &c.KEV, &when, &c.Elsewhere); err != nil {
			return nil, err
		}
		c.Released = when.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppleModel procura um modelo de Mac pelo identificador. Algumas entradas do SOFA têm
// sufixo ("MacPro7,1-Rack"), que o system_profiler não mostra: sem o nome exato, vale o
// primeiro com o mesmo identificador antes do hífen.
func (p *Postgres) AppleModel(ctx context.Context, model string) (compliance.MacModel, bool, error) {
	var m compliance.MacModel
	err := p.pool.QueryRow(ctx, `
		SELECT model, marketing_name, majors FROM apple_models
		WHERE model = $1 OR split_part(model, '-', 1) = $1
		ORDER BY (model = $1) DESC, model LIMIT 1`, model).Scan(&m.ID, &m.Name, &m.Majors)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// AppleModelsCount diz quantos modelos o catálogo tem (zero: o SOFA ainda não foi gravado
// com a seção Models).
func (p *Postgres) AppleModelsCount(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM apple_models`).Scan(&n)
	return n, err
}

// AppleExtraBuild procura um build entre as melhorias de segurança em segundo plano do gdmf
// ("25D771280a" é o 26.3.1 (a), sobre o 25D2128).
func (p *Postgres) AppleExtraBuild(ctx context.Context, build string) (compliance.MacExtra, bool, error) {
	var x compliance.MacExtra
	err := p.pool.QueryRow(ctx, `
		SELECT product_version, extra, prerequisite_build FROM apple_versions
		WHERE build = $1 AND extra <> '' LIMIT 1`, build).Scan(&x.Version, &x.Extra, &x.Prerequisite)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, false, nil
	}
	x.Build = build
	return x, err == nil, err
}
