package store

import (
	"context"
	"slices"
	"strings"
)

// BIOSFleetRow: o que a senha da BIOS em lote (docs/bios-senha.md, seção 12) precisa saber de
// uma máquina da frota, numa consulta só: o firmware do inventário atual, a caixa e os avisos
// do estado, e as senhas da guarda (da mais nova para a mais antiga).
type BIOSFleetRow struct {
	MachineID, Hostname string
	Ring                int
	Interface           string // a interface do fabricante ("" = nenhuma respondeu)
	AdminPassword       *bool  // a BIOS tem senha de administrador? nil: o inventário não diz
	SureAdmin           *bool
	BoxKey              string // a chave pública da caixa, como veio no estado
	Legacy              bool   // o Registro tem o marcador do .bat com a senha em uso (seção 11)
	CopyOK              bool   // o agente guarda a cópia de emergência
	Passwords           []BIOSPassword
}

// BIOSFleet lista as máquinas da frota (sem as aposentadas) para a senha da BIOS em lote, na
// ordem do nome.
func (p *Postgres) BIOSFleet(ctx context.Context) ([]BIOSFleetRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, coalesce(i.hostname, ''), m.ring, coalesce(i.fw_interface, ''), i.fw_admin_password,
		       (i.report #>> '{hardware,firmware,sure_admin}')::boolean,
		       coalesce(m.state ->> 'box_key', ''), coalesce((m.state ->> 'bios_legacy')::boolean, false),
		       coalesce((m.state ->> 'bios_copy')::boolean, false)
		FROM machines m
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE m.retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BIOSFleetRow
	at := map[string]int{}
	for rows.Next() {
		var r BIOSFleetRow
		if err := rows.Scan(&r.MachineID, &r.Hostname, &r.Ring, &r.Interface, &r.AdminPassword, &r.SureAdmin, &r.BoxKey, &r.Legacy, &r.CopyOK); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortBIOSFleet(out)
	for i, r := range out {
		at[r.MachineID] = i
	}
	pws, err := p.pool.Query(ctx, biosSelect+` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer pws.Close()
	for pws.Next() {
		b, _, err := scanBIOS(pws)
		if err != nil {
			return nil, err
		}
		if i, ok := at[b.MachineID]; ok {
			out[i].Passwords = append(out[i].Passwords, b)
		}
	}
	return out, pws.Err()
}

func sortBIOSFleet(list []BIOSFleetRow) {
	slices.SortFunc(list, func(a, b BIOSFleetRow) int {
		if c := strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname)); c != 0 {
			return c
		}
		return strings.Compare(a.MachineID, b.MachineID)
	})
}

// BIOSFleet na memória.
func (m *Memory) BIOSFleet(ctx context.Context) ([]BIOSFleetRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []BIOSFleetRow
	for id, mm := range m.machines {
		if !mm.retiredAt.IsZero() {
			continue
		}
		r := BIOSFleetRow{MachineID: id, Ring: mm.ring}
		if inv := mm.inventory; inv != nil {
			r.Hostname = inv.OS.Hostname
			if inv.Hardware != nil && inv.Hardware.Firmware != nil {
				fw := inv.Hardware.Firmware
				r.Interface, r.AdminPassword, r.SureAdmin = fw.Interface, fw.AdminPassword, fw.SureAdmin
			}
		}
		if s := mm.state; s != nil {
			r.BoxKey, r.Legacy, r.CopyOK = s.BoxKey, s.BIOSLegacy, s.BIOSCopy
		}
		for i := len(m.biosPasswords) - 1; i >= 0; i-- {
			if b := m.biosPasswords[i]; b.MachineID == id {
				r.Passwords = append(r.Passwords, cloneBIOS(b))
			}
		}
		out = append(out, r)
	}
	sortBIOSFleet(out)
	return out, nil
}
