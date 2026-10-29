package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type MachineReportRecord struct {
	ID     int64
	At     time.Time
	Report json.RawMessage
}

func historyTable(kind string) string {
	switch kind {
	case "inventario":
		return "inventory_reports"
	case "buscas":
		return "scan_reports"
	}
	return ""
}

// Pagination uses the original report ID. Only fixed table names can enter the query.
func (p *Postgres) MachineReportHistory(ctx context.Context, id, kind string, before int64) ([]MachineReportRecord, error) {
	table := historyTable(kind)
	if table == "" {
		return nil, fmt.Errorf("tipo de histórico inválido")
	}
	rows, err := p.pool.Query(ctx, "SELECT id,received_at FROM "+table+" WHERE machine_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT 51", id, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineReportRecord
	for rows.Next() {
		var r MachineReportRecord
		if err := rows.Scan(&r.ID, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (p *Postgres) MachineReportByID(ctx context.Context, id, kind string, report int64) (json.RawMessage, error) {
	table := historyTable(kind)
	if table == "" {
		return nil, fmt.Errorf("tipo de histórico inválido")
	}
	var data []byte
	err := p.pool.QueryRow(ctx, "SELECT report FROM "+table+" WHERE machine_id=$1 AND id=$2", id, report).Scan(&data)
	return data, err
}
func (m *Memory) MachineReportHistory(ctx context.Context, id, kind string, before int64) ([]MachineReportRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil || before != 0 {
		return nil, nil
	}
	switch kind {
	case "inventario":
		if mm.inventory != nil {
			return []MachineReportRecord{{ID: 1, At: mm.inventoryChangedAt}}, nil
		}
	case "buscas":
		if mm.scan != nil {
			return []MachineReportRecord{{ID: 1, At: mm.scanReceivedAt}}, nil
		}
	default:
		return nil, fmt.Errorf("tipo de histórico inválido")
	}
	return nil, nil
}
func (m *Memory) MachineReportByID(ctx context.Context, id, kind string, report int64) (json.RawMessage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil || report != 1 {
		return nil, ErrNoMachine
	}
	switch kind {
	case "inventario":
		if mm.inventory != nil {
			return json.Marshal(mm.inventory)
		}
	case "buscas":
		if mm.scan != nil {
			return json.Marshal(mm.scan)
		}
	}
	return nil, ErrNoMachine
}
