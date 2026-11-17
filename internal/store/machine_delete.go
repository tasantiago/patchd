package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Exclusão definitiva de uma máquina aposentada: o registro e o que é só dela (inventários,
// buscas, jobs, desempenho, pedidos de ligar). Só a aposentada sai, e nunca quando apagar
// perderia algo que não se recupera:
//   - a cópia guardada da senha da BIOS (a máquina pode estar no parque com outro registro);
//   - a mescla (o registro incorporado em outro guarda a história do que foi juntado);
//   - as sessões do Terminal (são a auditoria dos comandos dados na máquina).
// A trilha de auditoria do painel fica: ela guarda o ID como texto.

var (
	// ErrMachineNotFound: a máquina não existe.
	ErrMachineNotFound = errors.New("máquina não encontrada")
	// ErrNotRetired: só a máquina aposentada pode ser excluída.
	ErrNotRetired = errors.New("a máquina não está aposentada: aposente antes")
	// ErrKeepsBIOS: há cópia guardada da senha da BIOS.
	ErrKeepsBIOS = errors.New("a máquina tem a senha da BIOS guardada: o registro fica")
	// ErrMerged: o registro participa de uma mescla.
	ErrMerged = errors.New("a máquina foi incorporada (ou incorporou) outra: o registro fica")
	// ErrHasTerminal: há sessões do Terminal na máquina.
	ErrHasTerminal = errors.New("a máquina tem sessões do Terminal (auditoria): o registro fica")
)

// DeleteRetiredMachine exclui a máquina aposentada. ErrMachineNotFound se ela não existe.
func (p *Postgres) DeleteRetiredMachine(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var retired bool
	err = tx.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM machines WHERE id = $1 FOR UPDATE`, id).Scan(&retired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrMachineNotFound
	case err != nil:
		return err
	case !retired:
		return ErrNotRetired
	}
	var bios, merged, term bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM bios_passwords WHERE machine_id = $1),
		EXISTS (SELECT 1 FROM machine_merges WHERE source_id = $1 OR target_id = $1),
		EXISTS (SELECT 1 FROM terminal_sessions WHERE machine_id = $1)`, id).Scan(&bios, &merged, &term); err != nil {
		return err
	}
	switch {
	case bios:
		return ErrKeepsBIOS
	case merged:
		return ErrMerged
	case term:
		return ErrHasTerminal
	}
	// As tabelas sem ON DELETE CASCADE; as outras saem junto com a máquina.
	for _, t := range []string{"perf_alerts", "perf_eval", "perf_hourly", "perf_samples", "wake_requests", "campaign_wakes"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+t+` WHERE machine_id = $1`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM machines WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteRetiredMachine exclui a máquina aposentada (memória).
func (m *Memory) DeleteRetiredMachine(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	switch {
	case mm == nil:
		return ErrMachineNotFound
	case mm.retiredAt.IsZero():
		return ErrNotRetired
	}
	for _, b := range m.biosPasswords {
		if b.MachineID == id {
			return ErrKeepsBIOS
		}
	}
	for src, mg := range m.merges {
		if src == id || mg.TargetID == id {
			return ErrMerged
		}
	}
	if m.terminalData != nil {
		for _, s := range m.terminalData.sessions {
			if s.MachineID == id {
				return ErrHasTerminal
			}
		}
	}
	delete(m.machines, id)
	for k, v := range m.credentials {
		if v == id {
			delete(m.credentials, k)
		}
	}
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if j.MachineID != id {
			kept = append(kept, j)
		}
	}
	m.jobs = kept
	return nil
}
