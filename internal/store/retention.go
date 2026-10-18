package store

import (
	"context"
	"fmt"
	"time"
)

// Retention são os cortes da limpeza do banco (Aula 10.5).
type Retention struct {
	// History: inventários e buscas recebidos há mais que isso são apagados, exceto o
	// atual de cada máquina (que nunca sai, por mais velho que seja).
	History time.Duration
	// Jobs: jobs encerrados há mais que isso, e campanhas encerradas que ficaram sem
	// jobs, são apagados. Zero: guardar sempre (a política de auditoria decide).
	Jobs time.Duration
	// Audit (Aula 18.1): eventos da trilha de auditoria mais velhos que isso são apagados, e
	// a própria limpeza entra na trilha. Zero: guardar sempre.
	Audit time.Duration
}

// PruneResult conta as linhas apagadas (ou que seriam, na simulação) por tabela.
type PruneResult struct {
	Inventories int64
	Scans       int64
	Jobs        int64
	Campaigns   int64
	Audit       int64 // eventos da trilha de auditoria (Aula 18.1)
}

// Total soma todas.
func (r PruneResult) Total() int64 { return r.Inventories + r.Scans + r.Jobs + r.Campaigns + r.Audit }

// Prune aplica a retenção numa transação só. Com dry, a transação é desfeita no fim: as
// contagens são exatamente o que seria apagado, inclusive a ordem (jobs antes das
// campanhas que eles prendiam).
func (p *Postgres) Prune(ctx context.Context, r Retention, now time.Time, dry bool) (PruneResult, error) {
	var res PruneResult
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	exec := func(dst *int64, name, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("retenção (%s): %w", name, err)
		}
		*dst = tag.RowsAffected()
		return nil
	}
	history := now.Add(-r.History)
	if err := exec(&res.Inventories, "inventários", `
		DELETE FROM inventory_reports i
		WHERE i.received_at < $1
		  AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.current_inventory_id = i.id)`, history); err != nil {
		return res, err
	}
	if err := exec(&res.Scans, "buscas", `
		DELETE FROM scan_reports s
		WHERE s.received_at < $1
		  AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.current_scan_id = s.id)`, history); err != nil {
		return res, err
	}
	if r.Jobs > 0 {
		jobs := now.Add(-r.Jobs)
		// Só encerrados; os de campanha, só quando a campanha também acabou.
		if err := exec(&res.Jobs, "jobs", `
			DELETE FROM jobs j
			WHERE j.state NOT IN ('pendente', 'entregue')
			  AND coalesce(j.finished_at, j.created_at) < $1
			  AND (j.campaign_id IS NULL OR EXISTS (
			        SELECT 1 FROM campaigns c WHERE c.id = j.campaign_id AND c.state IN ('concluida', 'cancelada')))`, jobs); err != nil {
			return res, err
		}
		if err := exec(&res.Campaigns, "campanhas", `
			DELETE FROM campaigns c
			WHERE c.state IN ('concluida', 'cancelada')
			  AND c.updated_at < $1
			  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.campaign_id = c.id)`, jobs); err != nil {
			return res, err
		}
	}
	if r.Audit > 0 {
		cut := now.Add(-r.Audit)
		if err := exec(&res.Audit, "auditoria", `DELETE FROM audit_events WHERE at < $1`, cut); err != nil {
			return res, err
		}
		// Apagar a trilha também é uma ação, e fica nela, na mesma transação: ou as duas
		// coisas acontecem, ou nenhuma.
		if res.Audit > 0 && !dry {
			detail := fmt.Sprintf("%d evento(s) anteriores a %s apagados pela retenção de %d dias",
				res.Audit, cut.UTC().Format("2006-01-02"), int(r.Audit.Hours()/24))
			if _, err := tx.Exec(ctx, `
				INSERT INTO audit_events (at, username, source, action, detail) VALUES ($1, 'patchd-server', 'sistema', 'auditoria.retencao', $2)`,
				now.UTC(), detail); err != nil {
				return res, fmt.Errorf("retenção (registro na auditoria): %w", err)
			}
		}
	}
	if dry {
		return res, nil // o defer desfaz
	}
	return res, tx.Commit(ctx)
}

// TableSizes devolve o tamanho em disco de cada tabela do esquema (dados, índices e TOAST)
// e o do banco inteiro.
func (p *Postgres) TableSizes(ctx context.Context) (map[string]int64, int64, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT relname, pg_total_relation_size(relid)
		FROM pg_stat_user_tables WHERE schemaname = current_schema()`)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out[name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := p.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
