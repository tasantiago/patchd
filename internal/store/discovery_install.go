package store

import (
	"context"
	"time"
)

// DiscoveryCredential contains only ciphertext; the decryption key stays outside the database.
type DiscoveryCredential struct {
	Username  string
	Password  []byte
	Enabled   bool
	UpdatedBy string
}

type DiscoveryInstall struct {
	IP    string
	State string
	At    time.Time
	By    string
}

func (p *Postgres) DiscoveryCredential(ctx context.Context) (DiscoveryCredential, error) {
	var c DiscoveryCredential
	rows, err := p.pool.Query(ctx, "SELECT username, password, enabled, updated_by FROM discovery_credential WHERE id = 1")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	if rows.Next() {
		err = rows.Scan(&c.Username, &c.Password, &c.Enabled, &c.UpdatedBy)
	}
	if err != nil {
		return c, err
	}
	return c, rows.Err()
}

func (p *Postgres) SetDiscoveryCredential(ctx context.Context, c DiscoveryCredential) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO discovery_credential (id, username, password, enabled, updated_by)
 VALUES (1,$1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET username=$1,password=$2,enabled=$3,updated_by=$4`,
		c.Username, c.Password, c.Enabled, c.UpdatedBy)
	return err
}

func (p *Postgres) SaveDiscoveryInstall(ctx context.Context, d DiscoveryInstall) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO discovery_installs (ip,state,at,started_by) VALUES ($1,$2,$3,$4)
 ON CONFLICT (ip) DO UPDATE SET state=$2,at=$3,started_by=$4`, d.IP, d.State, d.At, d.By)
	return err
}

// ClaimDiscoveryInstall serializes attempts across server replicas and bounds retries.
func (p *Postgres) ClaimDiscoveryInstall(ctx context.Context, d DiscoveryInstall) (bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO discovery_installs (ip,state,at,started_by) VALUES ($1,'instalando',$2,$3)
 ON CONFLICT (ip) DO UPDATE SET state='instalando',at=$2,started_by=$3
 WHERE discovery_installs.at <= $2::timestamptz -
 CASE WHEN discovery_installs.state='aguardando registro' THEN interval '24 hours' ELSE interval '15 minutes' END`, d.IP, d.At, d.By)
	return tag.RowsAffected() > 0, err
}

func (p *Postgres) DiscoveryInstalls(ctx context.Context) ([]DiscoveryInstall, error) {
	rows, err := p.pool.Query(ctx, "SELECT host(ip),state,at,started_by FROM discovery_installs ORDER BY at DESC LIMIT 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryInstall
	for rows.Next() {
		var d DiscoveryInstall
		if err := rows.Scan(&d.IP, &d.State, &d.At, &d.By); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (m *Memory) DiscoveryCredential(ctx context.Context) (DiscoveryCredential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.discCredential
	c.Password = append([]byte(nil), c.Password...)
	return c, nil
}
func (m *Memory) SetDiscoveryCredential(ctx context.Context, c DiscoveryCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Password = append([]byte(nil), c.Password...)
	m.discCredential = c
	return nil
}
func (m *Memory) SaveDiscoveryInstall(ctx context.Context, d DiscoveryInstall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.discInstalls == nil {
		m.discInstalls = map[string]DiscoveryInstall{}
	}
	m.discInstalls[d.IP] = d
	return nil
}

func (m *Memory) ClaimDiscoveryInstall(ctx context.Context, d DiscoveryInstall) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.discInstalls[d.IP]; ok {
		delay := 15 * time.Minute
		if old.State == "aguardando registro" {
			delay = 24 * time.Hour
		}
		if d.At.Sub(old.At) < delay {
			return false, nil
		}
	}
	if m.discInstalls == nil {
		m.discInstalls = map[string]DiscoveryInstall{}
	}
	d.State = "instalando"
	m.discInstalls[d.IP] = d
	return true, nil
}
func (m *Memory) DiscoveryInstalls(ctx context.Context) ([]DiscoveryInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]DiscoveryInstall, 0, len(m.discInstalls))
	for _, d := range m.discInstalls {
		out = append(out, d)
	}
	return out, nil
}
