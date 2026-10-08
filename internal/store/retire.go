package store

import (
	"context"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// RetiredMachine é uma máquina aposentada (o tipo mora no protocolo, para a API).
type RetiredMachine = protocol.RetiredMachine

// RetireMachine aposenta a máquina. Devolve false se ela não existe ou já está aposentada.
// by é quem aposentou: o usuário do painel ou "linha de comando".
func (p *Postgres) RetireMachine(ctx context.Context, id, reason, by string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE machines SET retired_at = now(), retired_reason = $2, retired_by = $3 WHERE id = $1 AND retired_at IS NULL`, id, reason, by)
	return tag.RowsAffected() == 1, err
}

// RestoreMachine devolve a máquina à frota. Devolve false se ela não está aposentada.
func (p *Postgres) RestoreMachine(ctx context.Context, id string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE machines SET retired_at = NULL, retired_reason = NULL, retired_by = NULL WHERE id = $1 AND retired_at IS NOT NULL`, id)
	return tag.RowsAffected() == 1, err
}

// RetireMachine aposenta a máquina (memória).
func (m *Memory) RetireMachine(ctx context.Context, id, reason, by string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	if mm == nil || !mm.retiredAt.IsZero() {
		return false, nil
	}
	mm.retiredAt, mm.retiredReason, mm.retiredBy = m.now(), reason, by
	return true, nil
}

// RestoreMachine devolve a máquina à frota (memória).
func (m *Memory) RestoreMachine(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	if mm == nil || mm.retiredAt.IsZero() {
		return false, nil
	}
	mm.retiredAt, mm.retiredReason, mm.retiredBy = time.Time{}, "", ""
	return true, nil
}

// RetiredMachines lista as máquinas aposentadas (memória).
func (m *Memory) RetiredMachines(ctx context.Context) ([]RetiredMachine, error) {
	all := m.summaries(true)
	return all, nil
}
