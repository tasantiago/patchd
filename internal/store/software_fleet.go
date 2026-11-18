package store

import (
	"context"
	"slices"
	"strings"
)

// Os programas instalados no parque (Inventário › Programas): o software do inventário atual
// de cada máquina da frota (sem as aposentadas), junto por sistema, nome, fabricante e versão.
// A junção por programa (o nome sem a versão) fica no painel.

// SoftwareRow é uma combinação de software no parque e em quantas máquinas ela está.
type SoftwareRow struct {
	OSFamily, Name, Publisher, Version, Scope, Uninstall string
	System                                               bool // componente do sistema ou atualização de outro produto
	Machines                                             int
}

// SoftwareInstall é o programa numa máquina (a página do programa).
type SoftwareInstall struct {
	MachineID, Hostname                     string
	Name, Version, Scope, Uninstall, Source string
	ProductCode, User                       string
}

// FleetSoftware lista as combinações de software do parque. Uma consulta: o jsonb do
// inventário atual de cada máquina, aberto no banco.
func (p *Postgres) FleetSoftware(ctx context.Context) ([]SoftwareRow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT i.os_family, s->>'name', coalesce(s->>'publisher', ''), coalesce(s->>'version', ''), coalesce(s->>'scope', ''),
		       coalesce(s->>'uninstall', ''),
		       coalesce((s->>'system_component')::boolean, false) OR coalesce((s->>'is_update')::boolean, false),
		       count(DISTINCT m.id)
		FROM machines m
		JOIN inventory_reports i ON i.id = m.current_inventory_id
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(i.report->'software') = 'array'
		                                             THEN i.report->'software' ELSE '[]'::jsonb END) s
		WHERE m.retired_at IS NULL AND coalesce(s->>'name', '') <> ''
		GROUP BY 1, 2, 3, 4, 5, 6, 7`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SoftwareRow{}
	for rows.Next() {
		var r SoftwareRow
		if err := rows.Scan(&r.OSFamily, &r.Name, &r.Publisher, &r.Version, &r.Scope, &r.Uninstall, &r.System, &r.Machines); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SoftwareInstalls lista as máquinas com o software de um dos nomes (exatos) e do
// fabricante, no sistema.
func (p *Postgres) SoftwareInstalls(ctx context.Context, osFamily string, names []string, publisher string) ([]SoftwareInstall, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, coalesce(i.hostname, ''), s->>'name', coalesce(s->>'version', ''), coalesce(s->>'scope', ''),
		       coalesce(s->>'uninstall', ''), coalesce(s->>'source', ''), coalesce(s->>'product_code', ''), coalesce(s->>'user', '')
		FROM machines m
		JOIN inventory_reports i ON i.id = m.current_inventory_id
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(i.report->'software') = 'array'
		                                             THEN i.report->'software' ELSE '[]'::jsonb END) s
		WHERE m.retired_at IS NULL AND i.os_family = $1 AND s->>'name' = ANY($2) AND coalesce(s->>'publisher', '') = $3
		ORDER BY lower(coalesce(i.hostname, '')), m.id`, osFamily, names, publisher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SoftwareInstall{}
	for rows.Next() {
		var r SoftwareInstall
		if err := rows.Scan(&r.MachineID, &r.Hostname, &r.Name, &r.Version, &r.Scope, &r.Uninstall, &r.Source, &r.ProductCode, &r.User); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FleetSoftware na memória.
func (m *Memory) FleetSoftware(ctx context.Context) ([]SoftwareRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type key struct {
		os, name, pub, ver, scope, un string
		sys                           bool
	}
	count := map[key]map[string]bool{}
	for id, mm := range m.machines {
		if mm.inventory == nil || !mm.retiredAt.IsZero() {
			continue
		}
		for _, s := range mm.inventory.Software {
			if s.Name == "" {
				continue
			}
			k := key{mm.inventory.OS.Family, s.Name, s.Publisher, s.Version, s.Scope, s.Uninstall, s.SystemComponent || s.IsUpdate}
			if count[k] == nil {
				count[k] = map[string]bool{}
			}
			count[k][id] = true
		}
	}
	out := []SoftwareRow{}
	for k, ids := range count {
		out = append(out, SoftwareRow{OSFamily: k.os, Name: k.name, Publisher: k.pub, Version: k.ver, Scope: k.scope, Uninstall: k.un,
			System: k.sys, Machines: len(ids)})
	}
	return out, nil
}

// SoftwareInstalls na memória.
func (m *Memory) SoftwareInstalls(ctx context.Context, osFamily string, names []string, publisher string) ([]SoftwareInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []SoftwareInstall{}
	for id, mm := range m.machines {
		if mm.inventory == nil || !mm.retiredAt.IsZero() || mm.inventory.OS.Family != osFamily {
			continue
		}
		for _, s := range mm.inventory.Software {
			if slices.Contains(names, s.Name) && s.Publisher == publisher {
				out = append(out, SoftwareInstall{MachineID: id, Hostname: mm.inventory.OS.Hostname, Name: s.Name, Version: s.Version,
					Scope: s.Scope, Uninstall: s.Uninstall, Source: s.Source, ProductCode: s.ProductCode, User: s.User})
			}
		}
	}
	slices.SortFunc(out, func(a, b SoftwareInstall) int {
		if c := strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname)); c != 0 {
			return c
		}
		return strings.Compare(a.MachineID, b.MachineID)
	})
	return out, nil
}
