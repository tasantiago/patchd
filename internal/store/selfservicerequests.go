package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SelfServiceRequest (Aula 18.3): um pedido do autoatendimento que espera a TI.
type SelfServiceRequest struct {
	ID        int64
	MachineID string
	Account   string
	Groups    []string // os do token, no momento do pedido
	AppID     string
	Action    string // atualizar ou instalar
	Version   string // a versão que a loja oferecia no pedido
	State     string
	CreatedAt time.Time
	DecidedAt *time.Time
	DecidedBy string
	Note      string // motivo da recusa
	JobID     int64  // o job criado na aprovação
}

// Estados do pedido que espera a TI.
const (
	RequestWaiting  = "aguardando"
	RequestApproved = "aprovado"
	RequestRefused  = "recusado"
	RequestExpired  = "expirado"
)

// RequestTTL: sem decisão nesse prazo, o pedido expira.
const RequestTTL = 7 * 24 * time.Hour

// ErrRequestDecided: o pedido não está mais aguardando (alguém decidiu antes, ou expirou).
var ErrRequestDecided = errors.New("o pedido já foi decidido ou expirou")

// RequestFilter: Waiting lista os que esperam (de todas as máquinas); com MachineID e
// Account, os daquele usuário naquela máquina.
type RequestFilter struct {
	Waiting   bool
	MachineID string
	Account   string
	Limit     int
}

const requestCols = `id, machine_id, account, groups, app_id, action, version, state, created_at, decided_at, decided_by, note, coalesce(job_id, 0)`

func scanRequest(r pgx.CollectableRow) (SelfServiceRequest, error) {
	var q SelfServiceRequest
	err := r.Scan(&q.ID, &q.MachineID, &q.Account, &q.Groups, &q.AppID, &q.Action, &q.Version, &q.State, &q.CreatedAt,
		&q.DecidedAt, &q.DecidedBy, &q.Note, &q.JobID)
	q.CreatedAt, q.DecidedAt = q.CreatedAt.UTC(), utc(q.DecidedAt)
	if q.Groups == nil {
		q.Groups = []string{}
	}
	return q, err
}

// CreateSelfServiceRequest grava o pedido aguardando e devolve o ID.
func (p *Postgres) CreateSelfServiceRequest(ctx context.Context, q SelfServiceRequest) (int64, error) {
	if q.Groups == nil {
		q.Groups = []string{}
	}
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO selfservice_requests (machine_id, account, groups, app_id, action, version)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, q.MachineID, q.Account, q.Groups, q.AppID, q.Action, q.Version).Scan(&id)
	return id, err
}

// SelfServiceRequests lista os pedidos do filtro, do mais novo para o mais antigo.
func (p *Postgres) SelfServiceRequests(ctx context.Context, f RequestFilter) ([]SelfServiceRequest, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q := `SELECT ` + requestCols + ` FROM selfservice_requests WHERE `
	var args []any
	if f.Waiting {
		q += `state = 'aguardando'`
	} else {
		q += `machine_id = $1 AND lower(account) = lower($2)`
		args = append(args, f.MachineID, f.Account)
	}
	args = append(args, f.Limit)
	q += ` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanRequest)
}

// SelfServiceRequestByID devolve o pedido.
func (p *Postgres) SelfServiceRequestByID(ctx context.Context, id int64) (SelfServiceRequest, bool, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+requestCols+` FROM selfservice_requests WHERE id = $1`, id)
	if err != nil {
		return SelfServiceRequest{}, false, err
	}
	q, err := pgx.CollectExactlyOneRow(rows, scanRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false, nil
	}
	return q, err == nil, err
}

// DecideSelfServiceRequest grava a decisão de um pedido que ainda aguarda; outro estado:
// ErrRequestDecided (duas pessoas decidindo ao mesmo tempo: só a primeira vale).
func (p *Postgres) DecideSelfServiceRequest(ctx context.Context, id int64, state, by, note string, jobID int64, at time.Time) error {
	var job *int64
	if jobID > 0 {
		job = &jobID
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE selfservice_requests SET state = $2, decided_by = $3, note = $4, job_id = $5, decided_at = $6
		WHERE id = $1 AND state = 'aguardando'`, id, state, by, note, job, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRequestDecided
	}
	return nil
}

// ExpireSelfServiceRequests expira os que aguardam desde antes de before.
func (p *Postgres) ExpireSelfServiceRequests(ctx context.Context, before, now time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE selfservice_requests SET state = 'expirado', decided_at = $2, decided_by = 'patchd-server'
		WHERE state = 'aguardando' AND created_at < $1`, before, now)
	return tag.RowsAffected(), err
}

// Memória.

func (m *Memory) CreateSelfServiceRequest(ctx context.Context, q SelfServiceRequest) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.ID = int64(len(m.ssRequests) + 1)
	q.State, q.CreatedAt = RequestWaiting, m.now().UTC()
	q.Groups = slices.Clone(q.Groups)
	if q.Groups == nil {
		q.Groups = []string{}
	}
	m.ssRequests = append(m.ssRequests, q)
	return q.ID, nil
}

func (m *Memory) SelfServiceRequests(ctx context.Context, f RequestFilter) ([]SelfServiceRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	var out []SelfServiceRequest
	for i := len(m.ssRequests) - 1; i >= 0 && len(out) < f.Limit; i-- {
		q := m.ssRequests[i]
		if (f.Waiting && q.State == RequestWaiting) || (!f.Waiting && q.MachineID == f.MachineID && strings.EqualFold(q.Account, f.Account)) {
			out = append(out, q)
		}
	}
	return out, nil
}

func (m *Memory) SelfServiceRequestByID(ctx context.Context, id int64) (SelfServiceRequest, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id < 1 || id > int64(len(m.ssRequests)) {
		return SelfServiceRequest{}, false, nil
	}
	return m.ssRequests[id-1], true, nil
}

func (m *Memory) DecideSelfServiceRequest(ctx context.Context, id int64, state, by, note string, jobID int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || id > int64(len(m.ssRequests)) || m.ssRequests[id-1].State != RequestWaiting {
		return ErrRequestDecided
	}
	q := &m.ssRequests[id-1]
	t := at.UTC()
	q.State, q.DecidedBy, q.Note, q.JobID, q.DecidedAt = state, by, note, jobID, &t
	return nil
}

func (m *Memory) ExpireSelfServiceRequests(ctx context.Context, before, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for i := range m.ssRequests {
		if q := &m.ssRequests[i]; q.State == RequestWaiting && q.CreatedAt.Before(before) {
			t := now.UTC()
			q.State, q.DecidedAt, q.DecidedBy = RequestExpired, &t, "patchd-server"
			n++
		}
	}
	return n, nil
}
