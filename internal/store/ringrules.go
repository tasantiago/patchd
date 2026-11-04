package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Faixas de IP e nomes dos grupos de implantação (a página do grupo). Quem interpreta a faixa
// é o painel (internal/api), que sabe ler 10.1.2.0/24, 10.1.2.10-50 e 10.1.*; aqui ela é só o
// texto, conferido no tamanho e único.

// IPRing liga uma faixa de IPv4 a um grupo de implantação.
type IPRing struct {
	ID        int64
	Spec      string
	Ring      int
	UpdatedBy string
	UpdatedAt time.Time
}

// RingName é o nome de um grupo de implantação.
type RingName struct {
	Ring      int
	Name      string
	UpdatedBy string
	UpdatedAt time.Time
}

// Limites, como nos CHECK das tabelas.
const (
	MaxIPSpecLen   = 64
	MaxRingNameLen = 40
)

var (
	// ErrIPSpec: faixa vazia ou longa demais.
	ErrIPSpec = errors.New("faixa vazia ou com mais de 64 caracteres")
	// ErrIPSpecTaken: a faixa já está ligada a um grupo.
	ErrIPSpecTaken = errors.New("faixa já ligada a um grupo de implantação")
	// ErrRingName: nome longo demais.
	ErrRingName = errors.New("nome com mais de 40 caracteres")
)

// IPRings lista as faixas, pela ordem em que foram criadas.
func (p *Postgres) IPRings(ctx context.Context) ([]IPRing, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, spec, ring, updated_by, updated_at FROM ip_rings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IPRing{}
	for rows.Next() {
		var r IPRing
		if err := rows.Scan(&r.ID, &r.Spec, &r.Ring, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt = r.UpdatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddIPRing grava a faixa; ErrIPSpecTaken se ela já existe (em qualquer grupo).
func (p *Postgres) AddIPRing(ctx context.Context, r IPRing) (int64, error) {
	if r.Spec == "" || len(r.Spec) > MaxIPSpecLen {
		return 0, ErrIPSpec
	}
	if err := validRing(r.Ring); err != nil {
		return 0, err
	}
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO ip_rings (spec, ring, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (spec) DO NOTHING RETURNING id`, r.Spec, r.Ring, r.UpdatedBy).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrIPSpecTaken
	}
	return id, err
}

// DeleteIPRing remove a faixa e devolve como ela era; as máquinas ficam no grupo em que estão.
func (p *Postgres) DeleteIPRing(ctx context.Context, id int64) (IPRing, bool, error) {
	var r IPRing
	err := p.pool.QueryRow(ctx, `DELETE FROM ip_rings WHERE id = $1 RETURNING id, spec, ring, updated_by, updated_at`, id).
		Scan(&r.ID, &r.Spec, &r.Ring, &r.UpdatedBy, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return IPRing{}, false, nil
	}
	return r, err == nil, err
}

// RingNames lista os nomes, pelo número do grupo.
func (p *Postgres) RingNames(ctx context.Context) ([]RingName, error) {
	rows, err := p.pool.Query(ctx, `SELECT ring, name, updated_by, updated_at FROM ring_names ORDER BY ring`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RingName{}
	for rows.Next() {
		var n RingName
		if err := rows.Scan(&n.Ring, &n.Name, &n.UpdatedBy, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.UpdatedAt = n.UpdatedAt.UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetRingName grava (ou troca) o nome do grupo; nome vazio apaga.
func (p *Postgres) SetRingName(ctx context.Context, n RingName) error {
	if err := validRing(n.Ring); err != nil {
		return err
	}
	if utf8.RuneCountInString(n.Name) > MaxRingNameLen {
		return ErrRingName
	}
	if n.Name == "" {
		_, err := p.pool.Exec(ctx, `DELETE FROM ring_names WHERE ring = $1`, n.Ring)
		return err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO ring_names (ring, name, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (ring) DO UPDATE SET name = excluded.name, updated_by = excluded.updated_by, updated_at = now()`,
		n.Ring, n.Name, n.UpdatedBy)
	return err
}

// --- Memória -------------------------------------------------------------------------

// IPRings (memória).
func (m *Memory) IPRings(context.Context) ([]IPRing, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]IPRing{}, m.ipRings...)
	return out, nil
}

// AddIPRing (memória).
func (m *Memory) AddIPRing(_ context.Context, r IPRing) (int64, error) {
	if r.Spec == "" || len(r.Spec) > MaxIPSpecLen {
		return 0, ErrIPSpec
	}
	if err := validRing(r.Ring); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.ContainsFunc(m.ipRings, func(x IPRing) bool { return x.Spec == r.Spec }) {
		return 0, ErrIPSpecTaken
	}
	m.ipRingSeq++
	r.ID, r.UpdatedAt = m.ipRingSeq, m.now()
	m.ipRings = append(m.ipRings, r)
	return r.ID, nil
}

// DeleteIPRing (memória).
func (m *Memory) DeleteIPRing(_ context.Context, id int64) (IPRing, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.ipRings, func(x IPRing) bool { return x.ID == id })
	if i < 0 {
		return IPRing{}, false, nil
	}
	r := m.ipRings[i]
	m.ipRings = slices.Delete(m.ipRings, i, i+1)
	return r, true, nil
}

// RingNames (memória).
func (m *Memory) RingNames(context.Context) ([]RingName, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []RingName{}
	for _, n := range m.ringNames {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b RingName) int { return cmp.Compare(a.Ring, b.Ring) })
	return out, nil
}

// SetRingName (memória).
func (m *Memory) SetRingName(_ context.Context, n RingName) error {
	if err := validRing(n.Ring); err != nil {
		return err
	}
	if utf8.RuneCountInString(n.Name) > MaxRingNameLen {
		return ErrRingName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.Name == "" {
		delete(m.ringNames, n.Ring)
		return nil
	}
	if m.ringNames == nil {
		m.ringNames = map[int]RingName{}
	}
	n.UpdatedAt = m.now()
	m.ringNames[n.Ring] = n
	return nil
}
