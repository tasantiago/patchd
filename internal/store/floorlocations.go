package store

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FloorKey identifica uma faixa dentro de um prédio, sem depender do Shield.
type FloorKey struct {
	Building, Spec string
}

// FloorLocation define o nome físico; nome vazio remove o vínculo.
type FloorLocation struct {
	FloorKey
	Name string
}

func validFloorLocation(l FloorLocation) bool {
	return strings.TrimSpace(l.Building) != "" && len(l.Building) <= 500 &&
		strings.TrimSpace(l.Spec) != "" && len(l.Spec) <= 64 &&
		utf8.ValidString(l.Name) && utf8.RuneCountInString(l.Name) <= 40 &&
		strings.IndexFunc(l.Building, unicode.IsControl) < 0 &&
		strings.IndexFunc(l.Spec, unicode.IsControl) < 0 &&
		strings.IndexFunc(l.Name, unicode.IsControl) < 0
}

func (p *Postgres) FloorLocations(ctx context.Context) (map[FloorKey]string, error) {
	rows, err := p.pool.Query(ctx, "SELECT building, spec, name FROM floor_locations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[FloorKey]string{}
	for rows.Next() {
		var k FloorKey
		var name string
		if err := rows.Scan(&k.Building, &k.Spec, &name); err != nil {
			return nil, err
		}
		out[k] = name
	}
	return out, rows.Err()
}

func (p *Postgres) SaveFloorLocations(ctx context.Context, list []FloorLocation, by string) error {
	for _, l := range list {
		if !validFloorLocation(l) {
			return fmt.Errorf("localização de andar inválida")
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, l := range list {
		if l.Name == "" {
			_, err = tx.Exec(ctx, "DELETE FROM floor_locations WHERE building=$1 AND spec=$2", l.Building, l.Spec)
		} else {
			_, err = tx.Exec(ctx, "INSERT INTO floor_locations (building,spec,name,updated_by) VALUES ($1,$2,$3,$4) ON CONFLICT (building,spec) DO UPDATE SET name=EXCLUDED.name, updated_by=EXCLUDED.updated_by, updated_at=now()", l.Building, l.Spec, l.Name, by)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (m *Memory) FloorLocations(ctx context.Context) (map[FloorKey]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[FloorKey]string{}
	for k, v := range m.floorLocations {
		out[k] = v
	}
	return out, nil
}

func (m *Memory) SaveFloorLocations(ctx context.Context, list []FloorLocation, by string) error {
	for _, l := range list {
		if !validFloorLocation(l) {
			return fmt.Errorf("localização de andar inválida")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.floorLocations == nil {
		m.floorLocations = map[FloorKey]string{}
	}
	for _, l := range list {
		if l.Name == "" {
			delete(m.floorLocations, l.FloorKey)
		} else {
			m.floorLocations[l.FloorKey] = l.Name
		}
	}
	return nil
}
