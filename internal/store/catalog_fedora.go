package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
)

// FedoraLastPushed devolve a maior data de publicação (date_pushed) guardada de uma versão
// do Fedora; zero quando não há nenhum update dela.
func (p *Postgres) FedoraLastPushed(ctx context.Context, release string) (time.Time, error) {
	var t *time.Time
	if err := p.pool.QueryRow(ctx, `SELECT max(date_pushed) FROM fedora_updates WHERE release = $1`, release).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return t.UTC(), nil
}

// SaveFedoraUpdates grava os updates numa única transação, substituindo as versões
// anteriores de cada um. Ou entram todos, ou nenhum: a próxima sincronização parte da
// maior date_pushed guardada, e um lote gravado pela metade a faria pular o que faltou.
func (p *Postgres) SaveFedoraUpdates(ctx context.Context, us []bodhi.Update) error {
	if len(us) == 0 {
		return nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	aliases := make([]string, 0, len(us))
	seenU := map[string]bool{}
	var updates, builds, cves [][]any
	for _, u := range us {
		// O mesmo update em duas páginas (a lista mudou durante a paginação): fica uma vez.
		if seenU[u.Alias] {
			continue
		}
		seenU[u.Alias] = true
		aliases = append(aliases, u.Alias)
		updates = append(updates, []any{u.Alias, u.Release, u.Title, u.Severity, nullTime(u.Pushed), nullTime(u.Stable), u.CVEsPartial})
		seenB := map[string]bool{}
		for _, b := range u.Builds {
			if seenB[b.NVR] {
				continue
			}
			seenB[b.NVR] = true
			builds = append(builds, []any{u.Alias, b.NVR, b.Name, b.Epoch, b.Version, b.Release})
		}
		seenC := map[string]bool{}
		for _, c := range u.CVEs {
			if seenC[c] {
				continue
			}
			seenC[c] = true
			cves = append(cves, []any{u.Alias, c})
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM fedora_updates WHERE alias = ANY($1)`, aliases); err != nil {
		return err
	}
	copies := []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"fedora_updates", []string{"alias", "release", "title", "severity", "date_pushed", "date_stable", "cves_partial"}, updates},
		{"fedora_update_builds", []string{"alias", "nvr", "package", "epoch", "version", "rpm_release"}, builds},
		{"fedora_update_cves", []string{"alias", "cve"}, cves},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("%s: %w", c.table, err)
		}
	}
	return tx.Commit(ctx)
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// FedoraFix é uma linha do "catalog fedora": um build de um update de segurança.
type FedoraFix struct {
	Alias       string
	Release     string
	Stable      time.Time // zero quando o Bodhi não informa
	Severity    string
	NVR         string
	Epoch       int
	CVEs        int
	CVEsPartial bool
}

// FedoraFixes lista os updates de um pacote fonte, do mais novo para o mais velho;
// release (F44) filtra pela versão, vazio lista todas.
func (p *Postgres) FedoraFixes(ctx context.Context, pkg, release string) ([]FedoraFix, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT u.alias, u.release, u.date_stable, u.severity, b.nvr, b.epoch,
		       (SELECT count(*) FROM fedora_update_cves c WHERE c.alias = u.alias), u.cves_partial
		FROM fedora_update_builds b JOIN fedora_updates u ON u.alias = b.alias
		WHERE b.package = $1 AND ($2 = '' OR u.release = $2)
		ORDER BY u.date_pushed DESC NULLS LAST, u.alias`, pkg, release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FedoraFix
	for rows.Next() {
		var f FedoraFix
		var stable *time.Time
		if err := rows.Scan(&f.Alias, &f.Release, &stable, &f.Severity, &f.NVR, &f.Epoch, &f.CVEs, &f.CVEsPartial); err != nil {
			return nil, err
		}
		if stable != nil {
			f.Stable = stable.UTC()
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FedoraReleaseSummary resume uma versão do Fedora para o "catalog list".
type FedoraReleaseSummary struct {
	Release    string
	Updates    int
	Partial    int // updates com a lista de CVEs incompleta
	CVEs       int // CVEs distintas
	LastPushed time.Time
}

// FedoraSummary resume o catálogo do Fedora por versão.
func (p *Postgres) FedoraSummary(ctx context.Context) ([]FedoraReleaseSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT u.release, count(*), count(*) FILTER (WHERE u.cves_partial),
		       (SELECT count(DISTINCT c.cve) FROM fedora_update_cves c JOIN fedora_updates x ON x.alias = c.alias
		         WHERE x.release = u.release),
		       max(u.date_pushed)
		FROM fedora_updates u GROUP BY u.release ORDER BY u.release`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FedoraReleaseSummary
	for rows.Next() {
		var s FedoraReleaseSummary
		var last *time.Time
		if err := rows.Scan(&s.Release, &s.Updates, &s.Partial, &s.CVEs, &last); err != nil {
			return nil, err
		}
		if last != nil {
			s.LastPushed = last.UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
