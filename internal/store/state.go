package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Estado volátil da máquina (Aula 12.1): rede e discos do último check-in, sem histórico.

// HostState é o último estado guardado de uma máquina.
type HostState struct {
	State       *protocol.MachineState // nil: o agente ainda não mandou (anterior à 12.1)
	ReceivedAt  time.Time              // quando o servidor gravou State
	CheckinAddr string                 // de onde veio o último check-in; "" se nenhum desde a 0024
}

// cleanAddr aceita só um IP (sem porta); qualquer outra coisa vira "" (não gravado).
func cleanAddr(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	return ip.Unmap().String()
}

// SaveHostState grava o endereço do check-in e, se veio, o estado. state nil mantém o
// estado anterior (um agente antigo não apaga o que um novo mandou antes).
func (p *Postgres) SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error {
	var body []byte
	if state != nil {
		var err error
		if body, err = json.Marshal(state); err != nil {
			return err
		}
	}
	var a *string
	if c := cleanAddr(addr); c != "" {
		a = &c
	}
	_, err := p.pool.Exec(ctx, `
		UPDATE machines SET
			last_checkin_addr = coalesce($3::inet, last_checkin_addr),
			state    = coalesce($2::jsonb, state),
			state_at = CASE WHEN $2::jsonb IS NULL THEN state_at ELSE now() END
		WHERE id = $1`, id, body, a)
	return err
}

// HostState devolve o último estado da máquina; found false se ela não existe.
func (p *Postgres) HostState(ctx context.Context, id string) (HostState, bool, error) {
	var h HostState
	var body []byte
	var at *time.Time
	var addr *string
	err := p.pool.QueryRow(ctx, `SELECT state, state_at, host(last_checkin_addr) FROM machines WHERE id = $1`, id).
		Scan(&body, &at, &addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostState{}, false, nil
	}
	if err != nil {
		return HostState{}, false, err
	}
	if body != nil {
		var s protocol.MachineState
		if err := json.Unmarshal(body, &s); err != nil {
			return HostState{}, false, fmt.Errorf("estado guardado ilegível: %w", err)
		}
		h.State = &s
	}
	if at != nil {
		h.ReceivedAt = at.UTC()
	}
	h.CheckinAddr = deref(addr)
	return h, true, nil
}

// SaveHostState grava o endereço do check-in e, se veio, o estado.
func (m *Memory) SaveHostState(ctx context.Context, id string, state *protocol.MachineState, addr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	if mm == nil {
		return nil // como o UPDATE sem linha
	}
	if c := cleanAddr(addr); c != "" {
		mm.checkinAddr = c
	}
	if state != nil {
		// Cópia pelo JSON: quem chamou pode mexer no valor depois, e o Postgres também
		// devolve uma cópia.
		body, err := json.Marshal(state)
		if err != nil {
			return err
		}
		var s protocol.MachineState
		_ = json.Unmarshal(body, &s)
		mm.state, mm.stateAt = &s, m.now()
	}
	return nil
}

// HostState devolve o último estado da máquina.
func (m *Memory) HostState(ctx context.Context, id string) (HostState, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil {
		return HostState{}, false, nil
	}
	h := HostState{CheckinAddr: mm.checkinAddr, ReceivedAt: mm.stateAt}
	if mm.state != nil {
		s := *mm.state
		h.State = &s
	}
	return h, true, nil
}
