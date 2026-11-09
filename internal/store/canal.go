package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Canal em tempo real (internal/canal): os avisos de job novo saem do gatilho da 0062.

// ChannelName: o canal do NOTIFY.
const ChannelName = "patchd_canal"

// ListenJobs escuta os avisos de job novo e chama notify com o ID da máquina, até o contexto
// acabar ou a conexão cair (o chamador reconecta). ready é chamado depois do LISTEN.
func (p *Postgres) ListenJobs(ctx context.Context, ready func(), notify func(machineID string)) error {
	// Conexão própria, fora do pool: o LISTEN a prende o tempo todo, e com o pool pequeno (4)
	// seria uma vaga a menos para os check-ins.
	conn, err := p.sideConn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+ChannelName); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if n.Channel == ChannelName && len(n.Payload) <= 64 {
			notify(n.Payload)
		}
	}
}

// DueJobMachines: as máquinas com job pendente cujo início (janela, horário fixo) caiu em
// (from, to]. O canal as acorda: o job agendado sai na hora, não no próximo check-in.
func (p *Postgres) DueJobMachines(ctx context.Context, from, to time.Time) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT machine_id FROM jobs
		WHERE state = 'pendente' AND not_before > $1 AND not_before <= $2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OnJobCreated (memória, nos testes): o aviso que o gatilho dá no PostgreSQL.
func (m *Memory) OnJobCreated(f func(machineID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobHook = f
}

// SetCheckinRelay guarda por onde chegou o último check-in (true: pelo relé). Só escreve se mudou.
func (p *Postgres) SetCheckinRelay(ctx context.Context, id string, relay bool) error {
	_, err := p.pool.Exec(ctx, `UPDATE machines SET last_checkin_relay = $2 WHERE id = $1 AND last_checkin_relay IS DISTINCT FROM $2`, id, relay)
	return err
}

// CheckinRelay: por onde chegou o último check-in; nil: nenhum desde a 0064 (ou máquina desconhecida).
func (p *Postgres) CheckinRelay(ctx context.Context, id string) (*bool, error) {
	var v *bool
	err := p.pool.QueryRow(ctx, `SELECT last_checkin_relay FROM machines WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// SetCheckinRelay na memória.
func (m *Memory) SetCheckinRelay(ctx context.Context, id string, relay bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.machines[id]; mm != nil {
		v := relay
		mm.checkinRelay = &v
	}
	return nil
}

// CheckinRelay na memória.
func (m *Memory) CheckinRelay(ctx context.Context, id string) (*bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mm := m.machines[id]; mm != nil && mm.checkinRelay != nil {
		v := *mm.checkinRelay
		return &v, nil
	}
	return nil, nil
}
