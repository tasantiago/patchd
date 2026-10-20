package store

import (
	"context"
	"time"
)

// KeyStats (Aula 20.7, S8): quantas máquinas aceitam cada chave, pelo estado do último
// check-in. Antes de trocar a chave de jobs do servidor, o admin confere que a frota toda já
// aceita a nova; senão, os jobs das máquinas que faltam seriam recusados.
type KeyStats struct {
	Machines  int            // máquinas na frota (não aposentadas) com estado desde since
	Reporting int            // dessas, as que informam as chaves (agente da 20.7 ou mais novo)
	Job       map[string]int // impressão da chave de jobs → máquinas que a aceitam
	Release   map[string]int // impressão da chave de release → máquinas que a aceitam
}

// KeyStats conta as chaves aceitas pelas máquinas com estado desde since.
func (p *Postgres) KeyStats(ctx context.Context, since time.Time) (KeyStats, error) {
	ks := KeyStats{Job: map[string]int{}, Release: map[string]int{}}
	err := p.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE state ? 'job_keys' OR state ? 'release_keys')
		FROM machines WHERE retired_at IS NULL AND state_at >= $1`, since).Scan(&ks.Machines, &ks.Reporting)
	if err != nil {
		return ks, err
	}
	for campo, dest := range map[string]map[string]int{"job_keys": ks.Job, "release_keys": ks.Release} {
		rows, err := p.pool.Query(ctx, `
			SELECT k, count(*) FROM machines, jsonb_array_elements_text(state->`+"'"+campo+"'"+`) AS k
			WHERE retired_at IS NULL AND state_at >= $1 AND jsonb_typeof(state->`+"'"+campo+"'"+`) = 'array'
			GROUP BY k`, since)
		if err != nil {
			return ks, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return ks, err
			}
			dest[k] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return ks, err
		}
	}
	return ks, nil
}

// KeyStats na memória.
func (m *Memory) KeyStats(ctx context.Context, since time.Time) (KeyStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ks := KeyStats{Job: map[string]int{}, Release: map[string]int{}}
	for _, mm := range m.machines {
		if !mm.retiredAt.IsZero() || mm.state == nil || mm.stateAt.Before(since) {
			continue
		}
		ks.Machines++
		if mm.state.JobKeys != nil || mm.state.ReleaseKeys != nil {
			ks.Reporting++
		}
		for _, k := range mm.state.JobKeys {
			ks.Job[k]++
		}
		for _, k := range mm.state.ReleaseKeys {
			ks.Release[k]++
		}
	}
	return ks, nil
}
