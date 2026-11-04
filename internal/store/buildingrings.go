package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"
	"unicode/utf8"
)

// Grupo de implantação pelo prédio do patrimônio. O vínculo diz para que grupo vão as
// máquinas de um prédio; quem aplica é o painel (internal/api), que sabe ligar a máquina ao
// prédio pelo nome. As movidas à mão (SetMachineRing) ficam como exceção: ApplyRings não as
// mexe até UnpinRings devolvê-las ao vínculo.

// BuildingRing liga um prédio (o texto exato do patrimônio) a um grupo de implantação.
type BuildingRing struct {
	Building  string
	Ring      int
	UpdatedBy string
	UpdatedAt time.Time
}

// MaxBuildingLen: o tamanho do nome do prédio, como no CHECK da tabela.
const MaxBuildingLen = 200

// ErrBuilding: prédio vazio ou longo demais.
var ErrBuilding = errors.New("prédio vazio ou com mais de 200 caracteres")

func validBuilding(b BuildingRing) error {
	if b.Building == "" || utf8.RuneCountInString(b.Building) > MaxBuildingLen {
		return ErrBuilding
	}
	return validRing(b.Ring)
}

// BuildingRings lista os vínculos, pelo nome do prédio.
func (p *Postgres) BuildingRings(ctx context.Context) ([]BuildingRing, error) {
	rows, err := p.pool.Query(ctx, `SELECT building, ring, updated_by, updated_at FROM building_rings ORDER BY building`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BuildingRing{}
	for rows.Next() {
		var b BuildingRing
		if err := rows.Scan(&b.Building, &b.Ring, &b.UpdatedBy, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.UpdatedAt = b.UpdatedAt.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetBuildingRing grava (ou troca) o vínculo do prédio.
func (p *Postgres) SetBuildingRing(ctx context.Context, b BuildingRing) error {
	if err := validBuilding(b); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO building_rings (building, ring, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (building) DO UPDATE SET ring = excluded.ring, updated_by = excluded.updated_by, updated_at = now()`,
		b.Building, b.Ring, b.UpdatedBy)
	return err
}

// DeleteBuildingRing remove o vínculo; as máquinas ficam no grupo em que estão.
func (p *Postgres) DeleteBuildingRing(ctx context.Context, building string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM building_rings WHERE building = $1`, building)
	return tag.RowsAffected() == 1, err
}

// RingPins: as máquinas da frota movidas à mão.
func (p *Postgres) RingPins(ctx context.Context) (map[string]bool, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM machines WHERE ring_manual AND retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ApplyRings põe cada máquina (da frota, não movida à mão) no grupo do mapa; devolve quantas
// mudaram de grupo. Não marca como movida à mão.
func (p *Postgres) ApplyRings(ctx context.Context, target map[string]int) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	ids, rings := make([]string, 0, len(target)), make([]int16, 0, len(target))
	for id, r := range target {
		if err := validRing(r); err != nil {
			return 0, err
		}
		ids, rings = append(ids, id), append(rings, int16(r)) // #nosec G115 -- validRing: 0 a 9
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE machines m SET ring = t.ring
		FROM unnest($1::text[], $2::smallint[]) AS t(id, ring)
		WHERE m.id = t.id AND NOT m.ring_manual AND m.retired_at IS NULL AND m.ring <> t.ring`, ids, rings)
	return int(tag.RowsAffected()), err
}

// UnpinRings devolve as máquinas ao vínculo (deixam de ser exceção); devolve quantas eram.
func (p *Postgres) UnpinRings(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := p.pool.Exec(ctx, `UPDATE machines SET ring_manual = false WHERE id = ANY($1::text[]) AND ring_manual`, ids)
	return int(tag.RowsAffected()), err
}

// --- Memória -------------------------------------------------------------------------

// BuildingRings (memória).
func (m *Memory) BuildingRings(context.Context) ([]BuildingRing, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []BuildingRing{}
	for _, b := range m.buildingRings {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b BuildingRing) int { return cmp.Compare(a.Building, b.Building) })
	return out, nil
}

// SetBuildingRing (memória).
func (m *Memory) SetBuildingRing(_ context.Context, b BuildingRing) error {
	if err := validBuilding(b); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buildingRings == nil {
		m.buildingRings = map[string]BuildingRing{}
	}
	b.UpdatedAt = m.now()
	m.buildingRings[b.Building] = b
	return nil
}

// DeleteBuildingRing (memória).
func (m *Memory) DeleteBuildingRing(_ context.Context, building string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.buildingRings[building]
	delete(m.buildingRings, building)
	return ok, nil
}

// RingPins (memória).
func (m *Memory) RingPins(context.Context) (map[string]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]bool{}
	for id, mm := range m.machines {
		if mm.ringManual && mm.retiredAt.IsZero() {
			out[id] = true
		}
	}
	return out, nil
}

// ApplyRings (memória).
func (m *Memory) ApplyRings(_ context.Context, target map[string]int) (int, error) {
	for _, r := range target {
		if err := validRing(r); err != nil {
			return 0, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, r := range target {
		mm := m.machines[id]
		if mm == nil || !mm.retiredAt.IsZero() || mm.ringManual || mm.ring == r {
			continue
		}
		mm.ring = r
		n++
	}
	return n, nil
}

// UnpinRings (memória).
func (m *Memory) UnpinRings(_ context.Context, ids []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range ids {
		if mm := m.machines[id]; mm != nil && mm.ringManual {
			mm.ringManual = false
			n++
		}
	}
	return n, nil
}
