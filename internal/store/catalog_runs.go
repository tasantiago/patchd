package store

import (
	"context"
	"time"
)

// catalogLockKey identifica a trava do catálogo no PostgreSQL ("patchd" + 1). Qualquer
// processo ligado ao mesmo banco (o servidor, um catalog sync manual, uma segunda instância)
// disputa a mesma trava.
const catalogLockKey int64 = 0x7061746368640001

// TryCatalogLock tenta pegar a trava da sincronização do catálogo, sem esperar. A trava é
// de sessão: fica presa a uma conexão reservada do pool até unlock, e o PostgreSQL a solta
// sozinho se o processo morrer (a conexão cai). Devolve ok=false quando outro processo
// está sincronizando.
func (p *Postgres) TryCatalogLock(ctx context.Context) (unlock func(), ok bool, err error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, catalogLockKey).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, catalogLockKey); err != nil {
			// Sem o unlock, a conexão não pode voltar ao pool com a trava: fecha.
			conn.Hijack().Close(ctx)
			return
		}
		conn.Release()
	}, true, nil
}

// RecordCatalogRun registra a execução de uma fonte.
func (p *Postgres) RecordCatalogRun(ctx context.Context, source, trigger string, started, finished time.Time, ok bool, detail string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO catalog_runs (source, trigger, started_at, finished_at, ok, detail) VALUES ($1, $2, $3, $4, $5, $6)`,
		source, trigger, started, finished, ok, detail)
	return err
}

// CatalogSourceStatus é a situação de uma fonte do catálogo.
type CatalogSourceStatus struct {
	Source   string
	LastOK   time.Time // zero: nenhuma execução com sucesso registrada
	LastFail time.Time // zero: nenhuma falha registrada
	Latest   bool      // a execução mais recente deu certo
	Trigger  string    // da execução mais recente
	Detail   string    // da execução mais recente
	Runs     int       // execuções registradas
}

// CatalogStatus devolve a situação das fontes pedidas, na ordem pedida.
func (p *Postgres) CatalogStatus(ctx context.Context, sources []string) ([]CatalogSourceStatus, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT s.source,
		       (SELECT max(finished_at) FROM catalog_runs r WHERE r.source = s.source AND r.ok),
		       (SELECT max(finished_at) FROM catalog_runs r WHERE r.source = s.source AND NOT r.ok),
		       l.ok, coalesce(l.trigger, ''), coalesce(l.detail, ''),
		       (SELECT count(*) FROM catalog_runs r WHERE r.source = s.source)
		FROM unnest($1::text[]) WITH ORDINALITY AS s(source, pos)
		LEFT JOIN LATERAL (SELECT ok, trigger, detail FROM catalog_runs r WHERE r.source = s.source
		                   ORDER BY finished_at DESC, id DESC LIMIT 1) l ON true
		ORDER BY s.pos`, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogSourceStatus
	for rows.Next() {
		var s CatalogSourceStatus
		var lastOK, lastFail *time.Time
		var latest *bool
		if err := rows.Scan(&s.Source, &lastOK, &lastFail, &latest, &s.Trigger, &s.Detail, &s.Runs); err != nil {
			return nil, err
		}
		if lastOK != nil {
			s.LastOK = lastOK.UTC()
		}
		if lastFail != nil {
			s.LastFail = lastFail.UTC()
		}
		s.Latest = latest != nil && *latest
		out = append(out, s)
	}
	return out, rows.Err()
}

// PruneCatalogRuns apaga o registro de execuções mais velhas que before.
func (p *Postgres) PruneCatalogRuns(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM catalog_runs WHERE finished_at < $1`, before)
	return tag.RowsAffected(), err
}
