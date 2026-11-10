package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
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

// BIOSFleetSettings: o que o padrão da BIOS (docs/bios-config.md, seção 9) precisa de uma
// máquina além do BIOSFleetRow: as configurações da BIOS que interessam e o último job
// bios_settings.
type BIOSFleetSettings struct {
	Read     bool                   // o inventário atual tem as configurações da BIOS
	Settings []protocol.BIOSSetting // só as que casam com o filtro pedido
	// O último job bios_settings da máquina (LastJob 0: nenhum).
	LastJob      int64
	LastState    string
	LastFinished *time.Time
	LastPayload  string
}

// BIOSFleetSettings devolve, por máquina da frota, as configurações da BIOS cujo nome
// normalizado (sem os caracteres de strip, em minúsculas) casa com a expressão pattern, e o
// último job bios_settings.
func (p *Postgres) BIOSFleetSettings(ctx context.Context, pattern, strip string) (map[string]BIOSFleetSettings, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, coalesce(jsonb_typeof(i.report #> '{hardware,firmware,settings}') = 'array', false),
		       (SELECT coalesce(jsonb_agg(e.item ORDER BY e.ord), '[]'::jsonb)
		        FROM jsonb_array_elements(CASE WHEN jsonb_typeof(i.report #> '{hardware,firmware,settings}') = 'array'
		                                       THEN i.report #> '{hardware,firmware,settings}' ELSE '[]'::jsonb END)
		             WITH ORDINALITY AS e(item, ord)
		        WHERE translate(lower(e.item ->> 'name'), $2, '') ~ $1)
		FROM machines m
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		WHERE m.retired_at IS NULL`, pattern, strip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BIOSFleetSettings{}
	for rows.Next() {
		var id string
		var s BIOSFleetSettings
		var raw []byte
		if err := rows.Scan(&id, &s.Read, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Settings); err != nil {
			return nil, fmt.Errorf("configurações da BIOS de %s: %w", id, err)
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	last, err := p.pool.Query(ctx, `
		SELECT DISTINCT ON (machine_id) machine_id, id, state, finished_at, payload
		FROM jobs WHERE type = 'bios_settings' ORDER BY machine_id, id DESC`)
	if err != nil {
		return nil, err
	}
	defer last.Close()
	for last.Next() {
		var id string
		var j BIOSFleetSettings
		if err := last.Scan(&id, &j.LastJob, &j.LastState, &j.LastFinished, &j.LastPayload); err != nil {
			return nil, err
		}
		if s, ok := out[id]; ok {
			s.LastJob, s.LastState, s.LastFinished, s.LastPayload = j.LastJob, j.LastState, j.LastFinished, j.LastPayload
			out[id] = s
		}
	}
	return out, last.Err()
}

// BIOSFleetSettings na memória.
func (m *Memory) BIOSFleetSettings(ctx context.Context, pattern, strip string) (map[string]BIOSFleetSettings, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(strip, r) {
				return -1
			}
			return r
		}, strings.ToLower(s))
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]BIOSFleetSettings{}
	for id, mm := range m.machines {
		if !mm.retiredAt.IsZero() {
			continue
		}
		var s BIOSFleetSettings
		if inv := mm.inventory; inv != nil && inv.Hardware != nil && inv.Hardware.Firmware != nil && inv.Hardware.Firmware.Settings != nil {
			s.Read = true
			for _, st := range inv.Hardware.Firmware.Settings {
				if re.MatchString(norm(st.Name)) {
					s.Settings = append(s.Settings, st)
				}
			}
		}
		for _, j := range m.jobs {
			if j.MachineID == id && j.Type == "bios_settings" && j.ID > s.LastJob {
				s.LastJob, s.LastState, s.LastFinished, s.LastPayload = j.ID, j.State, j.FinishedAt, j.signed.Payload
			}
		}
		out[id] = s
	}
	return out, nil
}
