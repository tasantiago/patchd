package store

import (
	"context"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Aprovação de patches (Aula 13.1): quem aprovou cada item pendente, e quando.

// PatchApproval é a aprovação de um item com a sua correção.
type PatchApproval struct {
	Item, FixedIn string
	ApprovedBy    string
	ApprovedAt    time.Time
	Note          string
}

// ApprovalKey: a chave do item com a correção, a mesma nos dois armazenamentos e no painel.
func ApprovalKey(item, fixedIn string) string { return item + "\x00" + fixedIn }

// PatchApprovals lista as aprovações, as mais recentes primeiro.
func (p *Postgres) PatchApprovals(ctx context.Context) ([]PatchApproval, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT item, fixed_in, approved_by, approved_at, note FROM patch_approvals
		ORDER BY approved_at DESC, item COLLATE "C", fixed_in COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []PatchApproval{}
	for rows.Next() {
		var a PatchApproval
		if err := rows.Scan(&a.Item, &a.FixedIn, &a.ApprovedBy, &a.ApprovedAt, &a.Note); err != nil {
			return nil, err
		}
		a.ApprovedAt = a.ApprovedAt.UTC()
		list = append(list, a)
	}
	return list, rows.Err()
}

// ApprovePatch grava a aprovação; false se o item com essa correção já estava aprovado
// (a aprovação original fica: quem aprovou primeiro, e quando).
func (p *Postgres) ApprovePatch(ctx context.Context, a PatchApproval) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO patch_approvals (item, fixed_in, approved_by, note) VALUES ($1, $2, $3, $4)
		ON CONFLICT (item, fixed_in) DO NOTHING`, a.Item, a.FixedIn, a.ApprovedBy, a.Note)
	return tag.RowsAffected() == 1, err
}

// ApprovePatches aprova um lote numa transação só (Aula 13.4): ou o lote inteiro fica
// gravado, ou nada. Devolve as aprovações criadas; as que já existiam (aprovadas por
// outra pessoa entre a conferência e o clique) ficam como estavam e não voltam.
func (p *Postgres) ApprovePatches(ctx context.Context, list []PatchApproval) ([]PatchApproval, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit
	created := []PatchApproval{}
	for _, a := range list {
		tag, err := tx.Exec(ctx, `
			INSERT INTO patch_approvals (item, fixed_in, approved_by, note) VALUES ($1, $2, $3, $4)
			ON CONFLICT (item, fixed_in) DO NOTHING`, a.Item, a.FixedIn, a.ApprovedBy, a.Note)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 1 {
			created = append(created, a)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

// RevokePatch apaga a aprovação; false se ela não existia.
func (p *Postgres) RevokePatch(ctx context.Context, item, fixedIn string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM patch_approvals WHERE item = $1 AND fixed_in = $2`, item, fixedIn)
	return tag.RowsAffected() == 1, err
}

// PatchApprovals lista as aprovações, as mais recentes primeiro.
func (m *Memory) PatchApprovals(ctx context.Context) ([]PatchApproval, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]PatchApproval, 0, len(m.approvals))
	for _, a := range m.approvals {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].ApprovedAt.Equal(list[j].ApprovedAt) {
			return list[i].ApprovedAt.After(list[j].ApprovedAt)
		}
		return ApprovalKey(list[i].Item, list[i].FixedIn) < ApprovalKey(list[j].Item, list[j].FixedIn)
	})
	return list, nil
}

// ApprovePatch grava a aprovação; false se já existia.
func (m *Memory) ApprovePatch(ctx context.Context, a PatchApproval) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.approvals == nil {
		m.approvals = map[string]PatchApproval{}
	}
	k := ApprovalKey(a.Item, a.FixedIn)
	if _, ok := m.approvals[k]; ok {
		return false, nil
	}
	a.ApprovedAt = m.now()
	m.approvals[k] = a
	return true, nil
}

// ApprovePatches aprova um lote; devolve as aprovações criadas.
func (m *Memory) ApprovePatches(ctx context.Context, list []PatchApproval) ([]PatchApproval, error) {
	created := []PatchApproval{}
	for _, a := range list {
		ok, err := m.ApprovePatch(ctx, a)
		if err != nil {
			return nil, err
		}
		if ok {
			created = append(created, a)
		}
	}
	return created, nil
}

// RevokePatch apaga a aprovação; false se não existia.
func (m *Memory) RevokePatch(ctx context.Context, item, fixedIn string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := ApprovalKey(item, fixedIn)
	if _, ok := m.approvals[k]; !ok {
		return false, nil
	}
	delete(m.approvals, k)
	return true, nil
}

// ApprovedOnly fica com as pendências cuja correção está aprovada (Aula 13.2): é o filtro
// que o painel e as campanhas aplicam antes de criar um job update. A correção conta: um
// item aprovado em outra versão continua de fora.
func ApprovedOnly(items []protocol.PendingItem, approvals []PatchApproval) []protocol.PendingItem {
	ok := make(map[string]bool, len(approvals))
	for _, a := range approvals {
		ok[ApprovalKey(a.Item, a.FixedIn)] = true
	}
	out := []protocol.PendingItem{}
	for _, it := range items {
		if ok[ApprovalKey(it.ID, it.FixedIn)] {
			out = append(out, it)
		}
	}
	return out
}
