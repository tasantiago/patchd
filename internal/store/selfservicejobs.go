package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SelfServiceAuthorPrefix é o começo do autor dos jobs pedidos pelo usuário (Aula 17.2).
const SelfServiceAuthorPrefix = "autoatendimento: "

// SelfServiceJobs lista os jobs pedidos pelo autoatendimento desde since, do mais novo para
// o mais antigo, com o payload (o painel lê o programa e a versão dos parâmetros) (Aula 17.7).
func (p *Postgres) SelfServiceJobs(ctx context.Context, since time.Time, limit int) ([]JobRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, type, state, created_by, created_at, not_before, not_after, delivered_at, finished_at, detail, payload
		FROM jobs WHERE created_at >= $1 AND starts_with(created_by, $2) ORDER BY id DESC LIMIT $3`,
		since, SelfServiceAuthorPrefix, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (JobRow, error) {
		var j JobRow
		err := r.Scan(&j.ID, &j.MachineID, &j.Type, &j.State, &j.CreatedBy, &j.CreatedAt, &j.NotBefore, &j.NotAfter, &j.DeliveredAt,
			&j.FinishedAt, &j.Detail, &j.Payload)
		j.CreatedAt, j.NotAfter, j.DeliveredAt, j.FinishedAt = j.CreatedAt.UTC(), j.NotAfter.UTC(), utc(j.DeliveredAt), utc(j.FinishedAt)
		j.NotBefore = utc(j.NotBefore)
		return j, err
	})
}

// SelfServiceJobs lista os jobs do autoatendimento desde since.
func (m *Memory) SelfServiceJobs(ctx context.Context, since time.Time, limit int) ([]JobRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []JobRow
	for i := len(m.jobs) - 1; i >= 0 && len(out) < limit; i-- {
		j := m.jobs[i]
		if j.CreatedAt.Before(since) || !strings.HasPrefix(j.CreatedBy, SelfServiceAuthorPrefix) {
			continue
		}
		r := j.JobRow
		r.Payload = j.signed.Payload
		out = append(out, r)
	}
	return out, nil
}
