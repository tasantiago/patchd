package store

import (
	"context"
	"slices"
	"strings"
	"time"
)

// SourceFailure (Aula 16.2): uma fonte que falhou na busca atual de uma máquina. A busca
// com erro chega e é guardada como qualquer outra, mas sem lista: sem este resumo, uma
// máquina cujo WSUS recusa toda busca há uma semana passa despercebida.
type SourceFailure struct {
	MachineID, Hostname string
	Source, Error       string
	ScannedAt           time.Time  // a busca atual
	LastOK              *time.Time // a última busca guardada em que a fonte respondeu; nil: nenhuma (ou já apagada pela retenção)
}

// SourceFailures lista as fontes que falharam na busca atual das máquinas ativas (todas,
// com machineID vazio). Ordem: máquina, fonte.
func (p *Postgres) SourceFailures(ctx context.Context, machineID string) ([]SourceFailure, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, coalesce(i.hostname, ''), x->>'source', coalesce(x->>'error', ''), s.scanned_at,
		       (SELECT max(o.scanned_at) FROM scan_reports o
		          CROSS JOIN LATERAL jsonb_array_elements(o.report->'scans') y
		         WHERE o.machine_id = m.id AND y->>'source' = x->>'source' AND jsonb_typeof(y->'missing') = 'array')
		FROM machines m
		JOIN scan_reports s ON s.id = m.current_scan_id
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		CROSS JOIN LATERAL jsonb_array_elements(s.report->'scans') x
		WHERE m.retired_at IS NULL AND ($1 = '' OR m.id = $1)
		  AND jsonb_typeof(x->'missing') IS DISTINCT FROM 'array'
		ORDER BY m.id COLLATE "C", x->>'source'`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceFailure{}
	for rows.Next() {
		var f SourceFailure
		if err := rows.Scan(&f.MachineID, &f.Hostname, &f.Source, &f.Error, &f.ScannedAt, &f.LastOK); err != nil {
			return nil, err
		}
		f.ScannedAt = f.ScannedAt.UTC()
		f.LastOK = utc(f.LastOK)
		out = append(out, f)
	}
	return out, rows.Err()
}

// SourceFailures: na memória, a última busca em que cada fonte respondeu fica no
// registro da máquina (SaveScan).
func (m *Memory) SourceFailures(ctx context.Context, machineID string) ([]SourceFailure, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []SourceFailure{}
	for id, mm := range m.machines {
		if !mm.retiredAt.IsZero() || mm.scan == nil || (machineID != "" && id != machineID) {
			continue
		}
		host := ""
		if mm.inventory != nil {
			host = mm.inventory.OS.Hostname
		}
		for _, s := range mm.scan.Scans {
			if s.Missing != nil {
				continue
			}
			f := SourceFailure{MachineID: id, Hostname: host, Source: s.Source, Error: s.Error, ScannedAt: mm.scan.ScannedAt.UTC()}
			if t, ok := mm.sourceOK[s.Source]; ok {
				f.LastOK = &t
			}
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b SourceFailure) int {
		if c := strings.Compare(a.MachineID, b.MachineID); c != 0 {
			return c
		}
		return strings.Compare(a.Source, b.Source)
	})
	return out, nil
}
