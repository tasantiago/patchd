package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Lista de Equipamentos (Aula 12.2): uma linha por máquina da frota, com o que o inventário
// e o último check-in dizem dela. Filtro, ordem e páginas ficam no painel.

// InventoryRow é uma máquina da frota na lista de Equipamentos.
type InventoryRow struct {
	ID           string
	Hostname     string
	OSName       string
	OSVersion    string
	AgentVersion string
	LastSeenAt   time.Time
	CollectedAt  *time.Time // coleta do inventário atual; nil: nenhum inventário
	Ring         int
	CheckinTLS   *bool
	Manufacturer string
	Model        string
	Serial       string // cru, como o agente mandou (o painel descarta os genéricos)
	ChassisType  string // código SMBIOS cru
	State        *protocol.MachineState
	CheckinAddr  string
}

// InventoryList lista as máquinas da frota (sem as aposentadas) em ordem de ID. Uma
// consulta só; o relatório (jsonb) não é lido: o hardware vem das colunas da 0025.
func (p *Postgres) InventoryList(ctx context.Context) ([]InventoryRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, m.last_seen_at, coalesce(m.agent_version, ''), m.ring, m.last_checkin_tls,
		       coalesce(i.hostname, ''), coalesce(i.os_name, ''), coalesce(i.os_version, ''), i.collected_at,
		       coalesce(i.hw_manufacturer, ''), coalesce(i.hw_model, ''), coalesce(i.hw_serial, ''), coalesce(i.hw_chassis, ''),
		       m.state, coalesce(host(m.last_checkin_addr), '')
		FROM machines m
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE m.retired_at IS NULL
		ORDER BY m.id COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []InventoryRow{}
	for rows.Next() {
		var r InventoryRow
		var collected *time.Time
		var state []byte
		if err := rows.Scan(&r.ID, &r.LastSeenAt, &r.AgentVersion, &r.Ring, &r.CheckinTLS,
			&r.Hostname, &r.OSName, &r.OSVersion, &collected,
			&r.Manufacturer, &r.Model, &r.Serial, &r.ChassisType, &state, &r.CheckinAddr); err != nil {
			return nil, err
		}
		r.LastSeenAt, r.CollectedAt = r.LastSeenAt.UTC(), utc(collected)
		if state != nil {
			var s protocol.MachineState
			if err := json.Unmarshal(state, &s); err != nil {
				return nil, fmt.Errorf("estado guardado da máquina %s ilegível: %w", r.ID, err)
			}
			r.State = &s
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// InventoryList lista as máquinas da frota em ordem de ID.
func (m *Memory) InventoryList(ctx context.Context) ([]InventoryRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := []InventoryRow{}
	for id, mm := range m.machines {
		if !mm.retiredAt.IsZero() {
			continue
		}
		r := InventoryRow{ID: id, LastSeenAt: mm.lastSeenAt, AgentVersion: mm.agentVersion, Ring: mm.ring, CheckinAddr: mm.checkinAddr}
		if mm.checkinTLS != nil {
			v := *mm.checkinTLS
			r.CheckinTLS = &v
		}
		if inv := mm.inventory; inv != nil {
			t := inv.CollectedAt.UTC()
			r.Hostname, r.OSName, r.OSVersion, r.CollectedAt = inv.OS.Hostname, inv.OS.Name, inv.OS.Version, &t
			if hw := inv.Hardware; hw != nil {
				r.Manufacturer, r.Model, r.Serial, r.ChassisType = hw.Manufacturer, hw.Model, hw.SerialNumber, hw.ChassisType
			}
		}
		if mm.state != nil {
			s := *mm.state
			r.State = &s
		}
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}
