package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/campaign"
)

// Campanhas (Aula 8.5).

const campaignCols = `id, name, type, sources, rings, use_window, observe_sec, ttl_sec, state, current_idx, launched_idx,
	accepted_idx, ring_done_at, detail, created_by, created_at, updated_at`

func scanCampaign(row pgx.Row) (campaign.Campaign, error) {
	var c campaign.Campaign
	var rings []int16
	var observe, ttl int
	var cur, launched, accepted int16
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Sources, &rings, &c.UseWindow, &observe, &ttl, &c.State, &cur, &launched,
		&accepted, &c.RingDoneAt, &c.Detail, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	for _, r := range rings {
		c.Rings = append(c.Rings, int(r))
	}
	c.Observe, c.TTL = time.Duration(observe)*time.Second, time.Duration(ttl)*time.Second
	c.CurrentIdx, c.LaunchedIdx, c.AcceptedIdx = int(cur), int(launched), int(accepted)
	c.RingDoneAt = utc(c.RingDoneAt)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	if c.Sources == nil {
		c.Sources = []string{}
	}
	return c, nil
}

// CreateCampaign grava a campanha nova (em andamento, nenhum anel lançado) e devolve o ID.
func (p *Postgres) CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error) {
	if c.Sources == nil {
		c.Sources = []string{}
	}
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO campaigns (name, type, sources, rings, use_window, observe_sec, ttl_sec, state, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'em_andamento', $8) RETURNING id`,
		c.Name, c.Type, c.Sources, c.Rings, c.UseWindow, int(c.Observe/time.Second), int(c.TTL/time.Second), c.CreatedBy).Scan(&id)
	return id, err
}

// Campaign devolve a campanha.
func (p *Postgres) Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error) {
	c, err := scanCampaign(p.pool.QueryRow(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// Campaigns lista as campanhas, da mais nova para a mais antiga.
func (p *Postgres) Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error) {
	return p.campaigns(ctx, `SELECT `+campaignCols+` FROM campaigns ORDER BY id DESC LIMIT $1`, limit)
}

// RunningCampaigns lista as em andamento, da mais antiga para a mais nova.
func (p *Postgres) RunningCampaigns(ctx context.Context) ([]campaign.Campaign, error) {
	return p.campaigns(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE state = 'em_andamento' ORDER BY id`)
}

func (p *Postgres) campaigns(ctx context.Context, q string, args ...any) ([]campaign.Campaign, error) {
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []campaign.Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveCampaign grava o andamento, só se a campanha ainda estiver no estado from: o laço
// do servidor grava "em andamento"; se o admin pausou ou cancelou no meio, a gravação do
// laço não desfaz a decisão dele (devolve false).
func (p *Postgres) SaveCampaign(ctx context.Context, c campaign.Campaign, from string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE campaigns SET state = $2, current_idx = $3, launched_idx = $4, accepted_idx = $5, ring_done_at = $6,
			detail = $7, updated_at = now()
		WHERE id = $1 AND state = $8`, c.ID, c.State, c.CurrentIdx, c.LaunchedIdx, c.AcceptedIdx, c.RingDoneAt, c.Detail, from)
	return tag.RowsAffected() == 1, err
}

// CampaignJobs lista os jobs da campanha num anel (posição idx).
func (p *Postgres) CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, state, detail FROM jobs WHERE campaign_id = $1 AND campaign_idx = $2 ORDER BY id`, id, idx)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (campaign.JobState, error) {
		var j campaign.JobState
		err := r.Scan(&j.ID, &j.MachineID, &j.State, &j.Detail)
		return j, err
	})
}

// --- Memória -------------------------------------------------------------------------

// CreateCampaign grava a campanha nova.
func (m *Memory) CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.campaignSeq++
	c.ID, c.State, c.CurrentIdx, c.LaunchedIdx, c.AcceptedIdx = m.campaignSeq, campaign.StateRunning, 0, -1, -1
	c.CreatedAt, c.UpdatedAt = m.now(), m.now()
	if c.Sources == nil {
		c.Sources = []string{}
	}
	m.campaigns = append(m.campaigns, c)
	return c.ID, nil
}

// Campaign devolve a campanha.
func (m *Memory) Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.campaigns {
		if c.ID == id {
			return c, true, nil
		}
	}
	return campaign.Campaign{}, false, nil
}

// Campaigns lista as campanhas, da mais nova para a mais antiga.
func (m *Memory) Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []campaign.Campaign
	for i := len(m.campaigns) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.campaigns[i])
	}
	return out, nil
}

// RunningCampaigns lista as em andamento.
func (m *Memory) RunningCampaigns(ctx context.Context) ([]campaign.Campaign, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []campaign.Campaign
	for _, c := range m.campaigns {
		if c.State == campaign.StateRunning {
			out = append(out, c)
		}
	}
	return out, nil
}

// SaveCampaign grava o andamento, se a campanha ainda estiver no estado from.
func (m *Memory) SaveCampaign(ctx context.Context, c campaign.Campaign, from string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.campaigns {
		if m.campaigns[i].ID == c.ID && m.campaigns[i].State == from {
			c.UpdatedAt = m.now()
			m.campaigns[i] = c
			return true, nil
		}
	}
	return false, nil
}

// CampaignJobs lista os jobs da campanha num anel.
func (m *Memory) CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []campaign.JobState
	for _, j := range m.jobs {
		if j.campaignID == id && j.campaignIdx == idx {
			out = append(out, campaign.JobState{ID: j.ID, MachineID: j.MachineID, State: j.State, Detail: j.Detail})
		}
	}
	return out, nil
}
