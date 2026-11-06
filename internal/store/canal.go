package store

import (
	"context"
	"time"
)

// Canal em tempo real (internal/canal): os avisos de job novo saem do gatilho da 0062.

// ChannelName: o canal do NOTIFY.
const ChannelName = "patchd_canal"

// ListenJobs escuta os avisos de job novo e chama notify com o ID da máquina, até o contexto
// acabar ou a conexão cair (o chamador reconecta). ready é chamado depois do LISTEN.
func (p *Postgres) ListenJobs(ctx context.Context, ready func(), notify func(machineID string)) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+ChannelName); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// A conexão fica no meio de um LISTEN: descartada, não volta ao pool.
			_ = conn.Conn().Close(context.Background())
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
