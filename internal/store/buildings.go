package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/window"
)

// Prédios com ID estável e as janelas de manutenção por prédio (docs/implantacao-por-predio.md).
//
// O prédio é o DEPREDIO do patrimônio, exato: nenhuma normalização (maiúsculas, acentos,
// espaços) junta dois textos, para dois prédios diferentes nunca virarem um. Cada importação
// do patrimônio acrescenta os nomes novos e marca os ausentes sem apagá-los (a janela e o
// histórico continuam). Quando um prédio some e um nome novo aparece com os mesmos bens
// (renameOf), o ID fica e o nome muda: a janela, os andares e as campanhas seguem com ele.

// Building é um prédio do patrimônio.
type Building struct {
	ID          int64
	Name        string
	Present     bool // no patrimônio atual
	FirstSeen   time.Time
	LastSeen    time.Time
	RenamedFrom string // o nome anterior, quando a importação o reconheceu renomeado
}

// BuildingWindow é a janela de um prédio.
type BuildingWindow struct {
	BuildingID int64
	Window     window.Window
	Enabled    bool
	UpdatedBy  string
	UpdatedAt  time.Time
}

// MaxWindowBatch: quantos prédios uma gravação em lote aceita.
const MaxWindowBatch = 5000

// ErrUnknownBuilding: algum ID não é de um prédio cadastrado.
var ErrUnknownBuilding = errors.New("prédio desconhecido")

// renameOf acha os prédios renomeados entre duas importações: o prédio velho O sumiu, o nome N
// é novo (nem existia), e os bens batem dos dois lados — todos os bens de O que continuam no
// patrimônio estão em N, são ao menos metade dos bens de O e ao menos 90% dos de N — e nenhum
// outro prédio sumido cai em N. Qualquer dúvida: nenhuma troca (o prédio novo nasce com ID
// próprio e a janela fica no antigo, ausente).
func renameOf(old, cur []AssetLocation, known map[string]bool) map[string]string {
	oldBy, curBy := map[string][]string{}, map[string]int{}
	curOf := map[string]string{}
	for _, a := range old {
		if a.Building != "" {
			oldBy[a.Building] = append(oldBy[a.Building], a.Tombo)
		}
	}
	for _, a := range cur {
		if a.Building != "" {
			curOf[a.Tombo] = a.Building
			curBy[a.Building]++
		}
	}
	cand := map[string]string{}
	hits := map[string]int{}
	for o, tombos := range oldBy {
		if curBy[o] > 0 {
			continue // continua no patrimônio
		}
		target, shared, ok := "", 0, true
		for _, t := range tombos {
			n, there := curOf[t]
			if !there {
				continue
			}
			if target != "" && n != target {
				ok = false
				break
			}
			target = n
			shared++
		}
		if !ok || target == "" || known[target] || shared*2 < len(tombos) || shared*10 < curBy[target]*9 {
			continue
		}
		cand[o] = target
		hits[target]++
	}
	for o, n := range cand {
		if hits[n] != 1 {
			delete(cand, o)
		}
	}
	return cand
}

func validWindowRow(w window.Window) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if w.Days&window.AllDays == 0 {
		return errors.New("janela sem dias")
	}
	return nil
}

// --- PostgreSQL ------------------------------------------------------------------------

// syncBuildings, na transação da importação: troca os nomes dos renomeados, acrescenta os
// novos e marca presença. old é o patrimônio de antes.
func syncBuildings(ctx context.Context, tx pgx.Tx, old, cur []AssetLocation) error {
	rows, err := tx.Query(ctx, `SELECT name FROM buildings`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}
	ren := renameOf(old, cur, known)
	for _, o := range sortedKeys(ren) {
		n := ren[o]
		if _, err := tx.Exec(ctx, `UPDATE buildings SET name = $2, renamed_from = $1 WHERE name = $1`, o, n); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE floor_locations f SET building = $2 WHERE building = $1
			AND NOT EXISTS (SELECT 1 FROM floor_locations x WHERE x.building = $2 AND x.spec = f.spec)`, o, n); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE building_rings SET building = $2 WHERE building = $1
			AND NOT EXISTS (SELECT 1 FROM building_rings x WHERE x.building = $2)`, o, n); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO buildings (name) SELECT DISTINCT building FROM asset_locations WHERE building <> ''
		ON CONFLICT (name) DO UPDATE SET present = true, last_seen_at = now()`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE buildings SET present = false
		WHERE present AND name NOT IN (SELECT building FROM asset_locations WHERE building <> '')`)
	return err
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Buildings lista os prédios, pelo nome.
func (p *Postgres) Buildings(ctx context.Context) ([]Building, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, name, present, first_seen_at, last_seen_at, renamed_from FROM buildings ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Building, error) {
		var b Building
		err := r.Scan(&b.ID, &b.Name, &b.Present, &b.FirstSeen, &b.LastSeen, &b.RenamedFrom)
		b.FirstSeen, b.LastSeen = b.FirstSeen.UTC(), b.LastSeen.UTC()
		return b, err
	})
}

// BuildingWindows: a janela de cada prédio que tem (ativa ou não).
func (p *Postgres) BuildingWindows(ctx context.Context) (map[int64]BuildingWindow, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT building_id, days, start_min, duration_min, timezone, enabled, updated_by, updated_at FROM building_windows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]BuildingWindow{}
	for rows.Next() {
		var bw BuildingWindow
		var days, start, dur int
		var tz string
		if err := rows.Scan(&bw.BuildingID, &days, &start, &dur, &tz, &bw.Enabled, &bw.UpdatedBy, &bw.UpdatedAt); err != nil {
			return nil, err
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("janela do prédio %d: fuso %q: %w", bw.BuildingID, tz, err)
		}
		if days < 1 || days > 127 {
			return nil, fmt.Errorf("janela do prédio %d: dias %d fora de 1 a 127", bw.BuildingID, days)
		}
		bw.Window = window.Window{Days: window.Days(days), Start: time.Duration(start) * time.Minute,
			Duration: time.Duration(dur) * time.Minute, Location: loc}
		bw.UpdatedAt = bw.UpdatedAt.UTC()
		out[bw.BuildingID] = bw
	}
	return out, rows.Err()
}

// SetBuildingWindows grava a mesma janela (ativa) nos prédios, numa transação: todos ou nenhum.
func (p *Postgres) SetBuildingWindows(ctx context.Context, ids []int64, w window.Window, by string) error {
	if err := validWindowRow(w); err != nil {
		return err
	}
	if len(ids) == 0 || len(ids) > MaxWindowBatch {
		return fmt.Errorf("de 1 a %d prédios por gravação", MaxWindowBatch)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM buildings WHERE id = ANY($1)`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(uniqueIDs(ids)) {
		return ErrUnknownBuilding
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO building_windows (building_id, days, start_min, duration_min, timezone, enabled, updated_by)
		SELECT unnest($1::bigint[]), $2, $3, $4, $5, true, $6
		ON CONFLICT (building_id) DO UPDATE SET days = EXCLUDED.days, start_min = EXCLUDED.start_min,
			duration_min = EXCLUDED.duration_min, timezone = EXCLUDED.timezone, enabled = true,
			updated_by = EXCLUDED.updated_by, updated_at = now()`,
		uniqueIDs(ids), int(w.Days), int(w.Start/time.Minute), int(w.Duration/time.Minute), w.Location.String(), by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetBuildingWindowsEnabled ativa ou desativa (sem apagar) as janelas dos prédios; devolve
// quantas mudaram.
func (p *Postgres) SetBuildingWindowsEnabled(ctx context.Context, ids []int64, enabled bool, by string) (int, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE building_windows SET enabled = $2, updated_by = $3, updated_at = now()
		WHERE building_id = ANY($1) AND enabled <> $2`, ids, enabled, by)
	return int(tag.RowsAffected()), err
}

// DeleteBuildingWindows remove as janelas dos prédios; devolve quantas existiam.
func (p *Postgres) DeleteBuildingWindows(ctx context.Context, ids []int64) (int, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM building_windows WHERE building_id = ANY($1)`, ids)
	return int(tag.RowsAffected()), err
}

func uniqueIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// --- Memória ---------------------------------------------------------------------------

func (m *Memory) syncBuildingsLocked(old, cur []AssetLocation) {
	known := map[string]bool{}
	for _, b := range m.buildings {
		known[b.Name] = true
	}
	ren := renameOf(old, cur, known)
	for i := range m.buildings {
		if n, ok := ren[m.buildings[i].Name]; ok {
			o := m.buildings[i].Name
			m.buildings[i].RenamedFrom, m.buildings[i].Name = o, n
			for k, v := range m.floorLocations {
				if k.Building == o {
					nk := FloorKey{Building: n, Spec: k.Spec}
					if _, dup := m.floorLocations[nk]; !dup {
						delete(m.floorLocations, k)
						m.floorLocations[nk] = v
					}
				}
			}
		}
	}
	present := map[string]bool{}
	for _, a := range cur {
		if a.Building != "" {
			present[a.Building] = true
		}
	}
	now := m.now()
	for i := range m.buildings {
		b := &m.buildings[i]
		b.Present = present[b.Name]
		if b.Present {
			b.LastSeen = now
		}
		delete(present, b.Name)
	}
	for _, n := range slices.Sorted(maps.Keys(present)) {
		m.buildingSeq++
		m.buildings = append(m.buildings, Building{ID: m.buildingSeq, Name: n, Present: true, FirstSeen: now, LastSeen: now})
	}
}

// Buildings (memória).
func (m *Memory) Buildings(context.Context) ([]Building, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := slices.Clone(m.buildings)
	slices.SortFunc(out, func(a, b Building) int { return strings.Compare(a.Name, b.Name) })
	if out == nil {
		out = []Building{}
	}
	return out, nil
}

// BuildingWindows (memória).
func (m *Memory) BuildingWindows(context.Context) (map[int64]BuildingWindow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[int64]BuildingWindow, len(m.buildingWindows))
	for k, v := range m.buildingWindows {
		out[k] = v
	}
	return out, nil
}

// SetBuildingWindows (memória).
func (m *Memory) SetBuildingWindows(_ context.Context, ids []int64, w window.Window, by string) error {
	if err := validWindowRow(w); err != nil {
		return err
	}
	if len(ids) == 0 || len(ids) > MaxWindowBatch {
		return fmt.Errorf("de 1 a %d prédios por gravação", MaxWindowBatch)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if !slices.ContainsFunc(m.buildings, func(b Building) bool { return b.ID == id }) {
			return ErrUnknownBuilding
		}
	}
	if m.buildingWindows == nil {
		m.buildingWindows = map[int64]BuildingWindow{}
	}
	for _, id := range ids {
		m.buildingWindows[id] = BuildingWindow{BuildingID: id, Window: w, Enabled: true, UpdatedBy: by, UpdatedAt: m.now()}
	}
	return nil
}

// SetBuildingWindowsEnabled (memória).
func (m *Memory) SetBuildingWindowsEnabled(_ context.Context, ids []int64, enabled bool, by string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range uniqueIDs(ids) {
		if bw, ok := m.buildingWindows[id]; ok && bw.Enabled != enabled {
			bw.Enabled, bw.UpdatedBy, bw.UpdatedAt = enabled, by, m.now()
			m.buildingWindows[id] = bw
			n++
		}
	}
	return n, nil
}

// DeleteBuildingWindows (memória).
func (m *Memory) DeleteBuildingWindows(_ context.Context, ids []int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range uniqueIDs(ids) {
		if _, ok := m.buildingWindows[id]; ok {
			delete(m.buildingWindows, id)
			n++
		}
	}
	return n, nil
}
