package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Ligar pela rede (docs/wake-on-lan.md, Aula 22.2): os candidatos a repetidor e os pedidos.

// WakeHost é uma máquina candidata a repetir o pacote mágico.
type WakeHost struct {
	ID, Name string
	LastSeen time.Time
	State    *protocol.MachineState
}

// WakeRequest é um pedido de ligar.
type WakeRequest struct {
	ID          int64
	MachineID   string
	RequestedAt time.Time
	RequestedBy string
	Network     string
	MACs        []string
	Relays      []string // os IDs das máquinas que repetem
	JobIDs      []int64
	SentAt      *time.Time // o primeiro repetidor que mandou o pacote
	SentBy      string
	AnsweredAt  *time.Time // o primeiro check-in da máquina depois do pacote
}

// WakeAnswerWindow: um check-in até esse tempo depois do pacote conta como resposta.
const WakeAnswerWindow = 6 * time.Hour

// WakeRelayHosts lista as máquinas ativas com estado e check-in desde since, menos exclude.
func (p *Postgres) WakeRelayHosts(ctx context.Context, exclude string, since time.Time) ([]WakeHost, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, coalesce(i.hostname, ''), m.last_seen_at, m.state
		FROM machines m
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE m.retired_at IS NULL AND m.id <> $1 AND m.last_seen_at >= $2 AND m.state IS NOT NULL
		ORDER BY m.last_seen_at DESC`, exclude, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WakeHost
	for rows.Next() {
		var h WakeHost
		var body []byte
		if err := rows.Scan(&h.ID, &h.Name, &h.LastSeen, &body); err != nil {
			return nil, err
		}
		var s protocol.MachineState
		if err := json.Unmarshal(body, &s); err != nil {
			continue // um estado ilegível não derruba a lista
		}
		h.State, h.LastSeen = &s, h.LastSeen.UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// CreateWakeRequest grava o pedido e devolve o ID.
func (p *Postgres) CreateWakeRequest(ctx context.Context, r WakeRequest) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO wake_requests (machine_id, requested_at, requested_by, network, macs, relays, job_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		r.MachineID, r.RequestedAt.UTC(), r.RequestedBy, r.Network, r.MACs, r.Relays, r.JobIDs).Scan(&id)
	return id, err
}

// WakeRequests devolve os pedidos da máquina, do mais novo para o mais velho.
func (p *Postgres) WakeRequests(ctx context.Context, machineID string, limit int) ([]WakeRequest, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, requested_at, requested_by, network, macs, relays, job_ids, sent_at, coalesce(sent_by, ''), answered_at
		FROM wake_requests WHERE machine_id = $1 ORDER BY requested_at DESC, id DESC LIMIT $2`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WakeRequest
	for rows.Next() {
		var r WakeRequest
		if err := rows.Scan(&r.ID, &r.MachineID, &r.RequestedAt, &r.RequestedBy, &r.Network, &r.MACs, &r.Relays, &r.JobIDs,
			&r.SentAt, &r.SentBy, &r.AnsweredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkWakeSent anota o primeiro repetidor que mandou o pacote do pedido do job. false: o
// job não é de pedido nenhum, ou o pedido já tinha o primeiro.
func (p *Postgres) MarkWakeSent(ctx context.Context, jobID int64, relay string, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE wake_requests SET sent_at = $3, sent_by = $2
		WHERE $1 = ANY (job_ids) AND sent_at IS NULL`, jobID, relay, at.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkWakeAnswered anota o check-in da máquina nos pedidos com pacote mandado há pouco e
// ainda sem resposta.
func (p *Postgres) MarkWakeAnswered(ctx context.Context, machineID string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE wake_requests SET answered_at = $2
		WHERE machine_id = $1 AND answered_at IS NULL AND sent_at IS NOT NULL AND sent_at <= $2 AND sent_at >= $3`,
		machineID, at.UTC(), at.Add(-WakeAnswerWindow).UTC())
	return err
}

// --- Memória ------------------------------------------------------------------------

// WakeRelayHosts lista as máquinas ativas com estado e check-in desde since, menos exclude.
func (m *Memory) WakeRelayHosts(ctx context.Context, exclude string, since time.Time) ([]WakeHost, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []WakeHost
	for id, mm := range m.machines {
		if id == exclude || !mm.retiredAt.IsZero() || mm.state == nil || mm.lastSeenAt.Before(since) {
			continue
		}
		s := *mm.state
		h := WakeHost{ID: id, LastSeen: mm.lastSeenAt.UTC(), State: &s}
		if mm.inventory != nil {
			h.Name = mm.inventory.OS.Hostname
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CreateWakeRequest grava o pedido e devolve o ID.
func (m *Memory) CreateWakeRequest(ctx context.Context, r WakeRequest) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.machines[r.MachineID] == nil {
		return 0, fmt.Errorf("máquina %s desconhecida", r.MachineID)
	}
	r.ID = int64(len(m.wakeRequests) + 1)
	r.RequestedAt = r.RequestedAt.UTC()
	r.MACs, r.Relays, r.JobIDs = slices.Clone(r.MACs), slices.Clone(r.Relays), slices.Clone(r.JobIDs)
	m.wakeRequests = append(m.wakeRequests, r)
	return r.ID, nil
}

// WakeRequests devolve os pedidos da máquina, do mais novo para o mais velho.
func (m *Memory) WakeRequests(ctx context.Context, machineID string, limit int) ([]WakeRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []WakeRequest
	for i := len(m.wakeRequests) - 1; i >= 0 && len(out) < limit; i-- {
		if r := m.wakeRequests[i]; r.MachineID == machineID {
			out = append(out, r)
		}
	}
	return out, nil
}

// MarkWakeSent anota o primeiro repetidor que mandou o pacote.
func (m *Memory) MarkWakeSent(ctx context.Context, jobID int64, relay string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.wakeRequests {
		r := &m.wakeRequests[i]
		if r.SentAt == nil && slices.Contains(r.JobIDs, jobID) {
			t := at.UTC()
			r.SentAt, r.SentBy = &t, relay
			return true, nil
		}
	}
	return false, nil
}

// MarkWakeAnswered anota o check-in da máquina nos pedidos com pacote mandado há pouco.
func (m *Memory) MarkWakeAnswered(ctx context.Context, machineID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.wakeRequests {
		r := &m.wakeRequests[i]
		if r.MachineID == machineID && r.AnsweredAt == nil && r.SentAt != nil && !r.SentAt.After(at) && !r.SentAt.Before(at.Add(-WakeAnswerWindow)) {
			t := at.UTC()
			r.AnsweredAt = &t
		}
	}
	return nil
}
