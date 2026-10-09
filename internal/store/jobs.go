package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// maxJobsPerCheckin limita quantos jobs um check-in entrega.
const maxJobsPerCheckin = 10

// JobRow é um job como a tabela guarda (sem o payload, para listar).
type JobRow struct {
	ID          int64
	MachineID   string
	Type        string
	State       string
	CreatedBy   string
	CreatedAt   time.Time
	NotBefore   *time.Time // início da janela (Aula 8.3); nil = sem janela
	NotAfter    time.Time
	DeliveredAt *time.Time
	FinishedAt  *time.Time
	Detail      string
	Payload     string // só no JobForMachine: o servidor lê os parâmetros para conferir o efeito
}

// NewJob é o que a criação grava: o envelope assinado e as colunas para listar. É o
// mesmo tipo do pacote jobs, onde fica a criação (jobs.Issue) usada pela linha de
// comando e pelo painel.
type NewJob = jobs.Record

// NextJobID reserva o ID do próximo job: ele entra no payload assinado antes do INSERT.
func (p *Postgres) NextJobID(ctx context.Context) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('jobs', 'id'))`).Scan(&id)
	return id, err
}

// CreateJob grava o job pendente. A máquina é conferida no próprio INSERT: entre a
// escolha no painel e a gravação, ela pode ter sido aposentada.
func (p *Postgres) CreateJob(ctx context.Context, j NewJob) error {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO jobs (id, machine_id, type, state, payload, signature, created_by, not_after, not_before)
		SELECT $1, m.id, $3, 'pendente', $4, $5, $6, $7, $8
		FROM machines m WHERE m.id = $2 AND m.retired_at IS NULL`,
		j.ID, j.MachineID, j.Type, j.Signed.Payload, j.Signed.Signature, j.CreatedBy, j.NotAfter, nullTime(j.NotBefore))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return jobs.ErrMachineUnavailable
	}
	return nil
}

// ExpireJobs marca como expirados os jobs em aberto que passaram do prazo.
func (p *Postgres) ExpireJobs(ctx context.Context, now time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE jobs SET state = 'expirado', finished_at = $1, detail = 'prazo vencido sem resultado do agente'
		WHERE state IN ('pendente', 'entregue') AND not_after <= $1`, now)
	return tag.RowsAffected(), err
}

// DeliverJobs entrega os jobs pendentes da máquina (os com janela, só a partir do início dela) (no máximo maxJobsPerCheckin, em ordem
// de ID) e os marca como entregues. Entrega no máximo uma vez: um job entregue e sem
// resultado não volta a ser entregue; ele expira. Para mexer numa máquina, "no máximo
// uma vez" é mais seguro que "pelo menos uma vez".
func (p *Postgres) DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE jobs SET state = 'entregue', delivered_at = $2
		WHERE id IN (SELECT id FROM jobs WHERE machine_id = $1 AND state = 'pendente' AND not_after > $2
		             AND (not_before IS NULL OR not_before <= $2)
		             ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING id, payload, signature`, machineID, now, maxJobsPerCheckin)
	if err != nil {
		return nil, err
	}
	type delivered struct {
		id  int64
		env protocol.SignedJob
	}
	var list []delivered
	for rows.Next() {
		var d delivered
		if err := rows.Scan(&d.id, &d.env.Payload, &d.env.Signature); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := make([]protocol.SignedJob, len(list))
	for i, d := range list {
		out[i] = d.env
	}
	return out, nil
}

// JobForMachine devolve o job da máquina (para registrar o resultado).
func (p *Postgres) JobForMachine(ctx context.Context, machineID string, id int64) (JobRow, bool, error) {
	var j JobRow
	err := p.pool.QueryRow(ctx, `
		SELECT id, machine_id, type, state, created_by, created_at, not_before, not_after, delivered_at, finished_at, detail, payload
		FROM jobs WHERE id = $1 AND machine_id = $2`, id, machineID).
		Scan(&j.ID, &j.MachineID, &j.Type, &j.State, &j.CreatedBy, &j.CreatedAt, &j.NotBefore, &j.NotAfter, &j.DeliveredAt, &j.FinishedAt, &j.Detail, &j.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// FinishJob grava o estado final de um job ainda em aberto. Devolve false se ele já
// estava num estado final (resultado repetido, ou cancelado antes).
func (p *Postgres) FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE jobs SET state = $2, detail = $3, finished_at = $4
		WHERE id = $1 AND state IN ('pendente', 'entregue')`, id, state, detail, at)
	return tag.RowsAffected() == 1, err
}

// ScanReceivedAt devolve quando a busca atual da máquina chegou (nil: nenhuma).
func (p *Postgres) ScanReceivedAt(ctx context.Context, machineID string) (*time.Time, error) {
	var at *time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT s.received_at FROM machines m LEFT JOIN scan_reports s ON s.id = m.current_scan_id WHERE m.id = $1`, machineID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return utc(at), err
}

// Jobs lista os jobs, do mais novo para o mais antigo; machineID vazio: todas as máquinas.
func (p *Postgres) Jobs(ctx context.Context, machineID string, limit int) ([]JobRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, type, state, created_by, created_at, not_before, not_after, delivered_at, finished_at, detail
		FROM jobs WHERE ($1 = '' OR machine_id = $1) ORDER BY id DESC LIMIT $2`, machineID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (JobRow, error) {
		var j JobRow
		err := r.Scan(&j.ID, &j.MachineID, &j.Type, &j.State, &j.CreatedBy, &j.CreatedAt, &j.NotBefore, &j.NotAfter, &j.DeliveredAt, &j.FinishedAt, &j.Detail)
		j.CreatedAt, j.NotAfter, j.DeliveredAt, j.FinishedAt = j.CreatedAt.UTC(), j.NotAfter.UTC(), utc(j.DeliveredAt), utc(j.FinishedAt)
		j.NotBefore = utc(j.NotBefore)
		return j, err
	})
}

// CancelJob cancela um job pendente. Um job já entregue não é cancelado: o agente pode
// estar executando, e o resultado dele é o que vale.
func (p *Postgres) CancelJob(ctx context.Context, id int64, by string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE jobs SET state = 'cancelado', finished_at = now(), detail = 'cancelado por ' || $2
		WHERE id = $1 AND state = 'pendente'`, id, by)
	return tag.RowsAffected() == 1, err
}
