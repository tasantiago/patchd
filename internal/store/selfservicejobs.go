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
		FROM jobs WHERE created_at >= $1 AND starts_with(created_by, '`+SelfServiceAuthorPrefix+`') ORDER BY id DESC LIMIT $2`,
		since, limit) // o prefixo vai escrito na consulta: é o que deixa o índice parcial (0071) servir
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

// OpenSelfServiceJob diz se a máquina tem um job do autoatendimento em andamento (pendente ou
// entregue). O limite de downloads deixa passar os arquivos dele: quem pediu está esperando na
// frente da máquina. O índice dos jobs abertos (0018) atende.
func (p *Postgres) OpenSelfServiceJob(ctx context.Context, machineID string) (bool, error) {
	var ok bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM jobs WHERE machine_id = $1 AND state IN ('pendente', 'entregue')
		               AND starts_with(created_by, '`+SelfServiceAuthorPrefix+`'))`, machineID).Scan(&ok)
	return ok, err
}

// OpenSelfServiceJob diz se a máquina tem um job do autoatendimento em andamento.
func (m *Memory) OpenSelfServiceJob(ctx context.Context, machineID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, j := range m.jobs {
		if j.MachineID == machineID && (j.State == "pendente" || j.State == "entregue") && strings.HasPrefix(j.CreatedBy, SelfServiceAuthorPrefix) {
			return true, nil
		}
	}
	return false, nil
}
