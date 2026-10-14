package store

import (
	"context"
	"sort"
	"time"
)

// Aprovação de instaladores de terceiros (Aula 14.5).

// ThirdPartyApproval é a aprovação de um programa numa versão.
type ThirdPartyApproval struct {
	AppID, Version string
	ApprovedBy     string
	ApprovedAt     time.Time
	Note           string
}

// TPApprovalKey: programa e versão, a mesma chave nos dois armazenamentos e no painel.
func TPApprovalKey(app, version string) string { return app + "\x00" + version }

// ThirdPartyApprovals lista as aprovações, as mais recentes primeiro.
func (p *Postgres) ThirdPartyApprovals(ctx context.Context) ([]ThirdPartyApproval, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT app_id, version, approved_by, approved_at, note FROM thirdparty_approvals
		ORDER BY approved_at DESC, app_id COLLATE "C", version COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []ThirdPartyApproval{}
	for rows.Next() {
		var a ThirdPartyApproval
		if err := rows.Scan(&a.AppID, &a.Version, &a.ApprovedBy, &a.ApprovedAt, &a.Note); err != nil {
			return nil, err
		}
		a.ApprovedAt = a.ApprovedAt.UTC()
		list = append(list, a)
	}
	return list, rows.Err()
}

// ApproveThirdParty grava a aprovação; false se a versão já estava aprovada (a aprovação
// original fica).
func (p *Postgres) ApproveThirdParty(ctx context.Context, a ThirdPartyApproval) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO thirdparty_approvals (app_id, version, approved_by, note) VALUES ($1, $2, $3, $4)
		ON CONFLICT (app_id, version) DO NOTHING`, a.AppID, a.Version, a.ApprovedBy, a.Note)
	return tag.RowsAffected() == 1, err
}

// RevokeThirdParty apaga a aprovação; false se ela não existia.
func (p *Postgres) RevokeThirdParty(ctx context.Context, app, version string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM thirdparty_approvals WHERE app_id = $1 AND version = $2`, app, version)
	return tag.RowsAffected() == 1, err
}

// ThirdPartyApprovals lista as aprovações (memória).
func (m *Memory) ThirdPartyApprovals(context.Context) ([]ThirdPartyApproval, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]ThirdPartyApproval, 0, len(m.tpApprovals))
	for _, a := range m.tpApprovals {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].ApprovedAt.Equal(list[j].ApprovedAt) {
			return list[i].ApprovedAt.After(list[j].ApprovedAt)
		}
		return TPApprovalKey(list[i].AppID, list[i].Version) < TPApprovalKey(list[j].AppID, list[j].Version)
	})
	return list, nil
}

// ApproveThirdParty grava a aprovação (memória).
func (m *Memory) ApproveThirdParty(_ context.Context, a ThirdPartyApproval) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tpApprovals == nil {
		m.tpApprovals = map[string]ThirdPartyApproval{}
	}
	k := TPApprovalKey(a.AppID, a.Version)
	if _, ok := m.tpApprovals[k]; ok {
		return false, nil
	}
	a.ApprovedAt = m.now()
	m.tpApprovals[k] = a
	return true, nil
}

// RevokeThirdParty apaga a aprovação (memória).
func (m *Memory) RevokeThirdParty(_ context.Context, app, version string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := TPApprovalKey(app, version)
	if _, ok := m.tpApprovals[k]; !ok {
		return false, nil
	}
	delete(m.tpApprovals, k)
	return true, nil
}
