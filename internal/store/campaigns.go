package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/window"
)

// Campanhas (Aula 8.5).

const campaignCols = `id, name, type, sources, rings, use_window, observe_sec, ttl_sec, state, current_idx, launched_idx,
	accepted_idx, ring_done_at, detail, created_by, created_at, updated_at, app, version, notes, reboot_delay_sec,
	daily_start_min, daily_duration_min, daily_tz, microsoft_fallback, close_delay_sec, auto_resend, archived_at`

func scanCampaign(row pgx.Row) (campaign.Campaign, error) {
	var c campaign.Campaign
	var rings []int16
	var observe, ttl, delay, closeDelay int
	var cur, launched, accepted, autoResend int16
	var dStart, dDur *int
	var dTZ *string
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Sources, &rings, &c.UseWindow, &observe, &ttl, &c.State, &cur, &launched,
		&accepted, &c.RingDoneAt, &c.Detail, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.App, &c.Version, &c.Notes, &delay,
		&dStart, &dDur, &dTZ, &c.MicrosoftFallback, &closeDelay, &autoResend, &c.ArchivedAt)
	if err != nil {
		return c, err
	}
	if dStart != nil && dDur != nil && dTZ != nil {
		loc, err := time.LoadLocation(*dTZ)
		if err != nil {
			return c, fmt.Errorf("campanha %d: fuso do horário fixo %q: %w", c.ID, *dTZ, err)
		}
		c.Daily = &window.Window{Days: window.AllDays, Start: time.Duration(*dStart) * time.Minute,
			Duration: time.Duration(*dDur) * time.Minute, Location: loc}
	}
	for _, r := range rings {
		c.Rings = append(c.Rings, int(r))
	}
	c.Observe, c.TTL = time.Duration(observe)*time.Second, time.Duration(ttl)*time.Second
	c.RebootDelay = time.Duration(delay) * time.Second
	c.CloseDelay = time.Duration(closeDelay) * time.Second
	c.AutoResend = int(autoResend)
	c.ArchivedAt = utc(c.ArchivedAt)
	c.CurrentIdx, c.LaunchedIdx, c.AcceptedIdx = int(cur), int(launched), int(accepted)
	c.RingDoneAt = utc(c.RingDoneAt)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	if c.Sources == nil {
		c.Sources = []string{}
	}
	c.Notes = notes(c.Notes)
	return c, nil
}

// CreateCampaign grava a campanha nova (em andamento, nenhum grupo de implantação lançado) e devolve o ID.
func (p *Postgres) CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error) {
	if c.Sources == nil {
		c.Sources = []string{}
	}
	var dStart, dDur *int
	var dTZ *string
	if c.Daily != nil {
		st, du, tz := int(c.Daily.Start/time.Minute), int(c.Daily.Duration/time.Minute), c.Daily.Location.String()
		dStart, dDur, dTZ = &st, &du, &tz
	}
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO campaigns (name, type, sources, rings, use_window, observe_sec, ttl_sec, state, created_by, app, version, reboot_delay_sec,
			daily_start_min, daily_duration_min, daily_tz, microsoft_fallback, close_delay_sec, auto_resend)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'em_andamento', $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
		c.Name, c.Type, c.Sources, c.Rings, c.UseWindow, int(c.Observe/time.Second), int(c.TTL/time.Second), c.CreatedBy,
		c.App, c.Version, int(c.RebootDelay/time.Second), dStart, dDur, dTZ, c.MicrosoftFallback, int(c.CloseDelay/time.Second), c.AutoResend).Scan(&id)
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
			detail = $7, notes = $9, updated_at = now()
		WHERE id = $1 AND state = $8`, c.ID, c.State, c.CurrentIdx, c.LaunchedIdx, c.AcceptedIdx, c.RingDoneAt, c.Detail, from, notes(c.Notes))
	return tag.RowsAffected() == 1, err
}

// RequestResend grava o pedido de reenvio do admin (os motivos) e põe a campanha em andamento,
// só se ela ainda estiver no estado from. O motor pega o pedido no próximo minuto (TakeResend).
func (p *Postgres) RequestResend(ctx context.Context, id int64, reasons []string, detail, from string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE campaigns SET resend = $2, state = 'em_andamento', detail = $3, updated_at = now()
		WHERE id = $1 AND state = $4`, id, reasons, detail, from)
	return tag.RowsAffected() == 1, err
}

// TakeResend devolve e esvazia o pedido de reenvio (nil: nenhum), numa operação só: o pedido
// feito enquanto o motor trabalha fica para o minuto seguinte, não se perde.
func (p *Postgres) TakeResend(ctx context.Context, id int64) ([]string, error) {
	var out []string
	err := p.pool.QueryRow(ctx, `
		UPDATE campaigns c SET resend = '{}' FROM (SELECT id, resend FROM campaigns WHERE id = $1 AND resend <> '{}' FOR UPDATE) o
		WHERE c.id = o.id RETURNING o.resend`, id).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

// ArchiveCampaigns arquiva (ou, com archive false, desarquiva) as campanhas encerradas dos IDs.
// Devolve quantas mudaram; a em andamento ou pausada fica como está.
func (p *Postgres) ArchiveCampaigns(ctx context.Context, ids []int64, archive bool, at time.Time) (int, error) {
	if !archive {
		tag, err := p.pool.Exec(ctx, `UPDATE campaigns SET archived_at = NULL WHERE id = ANY($1) AND archived_at IS NOT NULL`, ids)
		return int(tag.RowsAffected()), err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE campaigns SET archived_at = $2
		WHERE id = ANY($1) AND state IN ('concluida', 'cancelada') AND archived_at IS NULL`, ids, at)
	return int(tag.RowsAffected()), err
}

// DeleteCampaign exclui a campanha arquivada (e encerrada). Os jobs dela ficam no histórico,
// sem a ligação com a campanha. false: não existe, ou não está arquivada.
func (p *Postgres) DeleteCampaign(ctx context.Context, id int64) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM campaigns WHERE id = $1 AND archived_at IS NOT NULL
		AND state IN ('concluida', 'cancelada') FOR UPDATE)`, id).Scan(&ok); err != nil || !ok {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET campaign_id = NULL, campaign_idx = NULL WHERE campaign_id = $1`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM campaigns WHERE id = $1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// notes: o text[] não aceita NULL.
func notes(n []string) []string {
	if n == nil {
		return []string{}
	}
	return n
}

// CampaignJobs lista os jobs da campanha num grupo de implantação (posição idx).
func (p *Postgres) CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, machine_id, state, detail, finished_at, type,
			CASE WHEN type = 'thirdparty' THEN coalesce(payload::jsonb #>> '{params,app}', '') ELSE '' END, unblocked_at
		FROM jobs WHERE campaign_id = $1 AND campaign_idx = $2 ORDER BY id`, id, idx)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (campaign.JobState, error) {
		var j campaign.JobState
		var typ string
		// O slot (o programa) sai do payload no banco: a campanha roda a cada minuto, e trazer
		// o payload assinado de milhares de jobs para ler um campo pesava.
		err := r.Scan(&j.ID, &j.MachineID, &j.State, &j.Detail, &j.FinishedAt, &typ, &j.Slot, &j.Unblocked)
		j.FinishedAt, j.Unblocked = utc(j.FinishedAt), utc(j.Unblocked)
		return j, err
	})
}

// DismissJob encerra um job adiado (ou falho, expirado, recusado, no reenvio) cuja nova
// tentativa não faz mais sentido (Aula 14.9): a máquina já não precisa. Vira "dispensado",
// com o motivo acrescentado ao detalhe.
func (p *Postgres) DismissJob(ctx context.Context, id int64, why string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE jobs SET state = 'dispensado', detail = detail || '; ' || $2
		WHERE id = $1 AND state IN ('adiado', 'falhou', 'expirado', 'rejeitado')`, id, why)
	return tag.RowsAffected() == 1, err
}

// MarkJobsUnblocked (tentar assim que fechar): o agente avisou que o programa que adiou estes
// jobs fechou. Só os adiados da própria máquina, uma vez cada. Devolve quantos.
func (p *Postgres) MarkJobsUnblocked(ctx context.Context, machineID string, ids []int64, at time.Time) (int, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE jobs SET unblocked_at = $3
		WHERE machine_id = $1 AND id = ANY($2) AND state = 'adiado' AND unblocked_at IS NULL`, machineID, ids, at)
	return int(tag.RowsAffected()), err
}

// SetMachineCapabilities guarda o que o agente anunciou no check-in (só escreve se mudou).
func (p *Postgres) SetMachineCapabilities(ctx context.Context, id string, caps []string) error {
	if caps == nil {
		caps = []string{}
	}
	_, err := p.pool.Exec(ctx, `UPDATE machines SET capabilities = $2 WHERE id = $1 AND capabilities IS DISTINCT FROM $2`, id, caps)
	return err
}

// MachineCapabilities: o que o agente anunciou no último check-in.
func (p *Postgres) MachineCapabilities(ctx context.Context, id string) ([]string, error) {
	var caps []string
	err := p.pool.QueryRow(ctx, `SELECT capabilities FROM machines WHERE id = $1`, id).Scan(&caps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return caps, err
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
	c.Notes = []string{}
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
			c.Notes = notes(slices.Clone(c.Notes))
			m.campaigns[i] = c
			return true, nil
		}
	}
	return false, nil
}

// RequestResend grava o pedido de reenvio.
func (m *Memory) RequestResend(ctx context.Context, id int64, reasons []string, detail, from string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.campaigns {
		if c := &m.campaigns[i]; c.ID == id && c.State == from {
			c.State, c.Detail, c.UpdatedAt = campaign.StateRunning, detail, m.now()
			if m.campaignResend == nil {
				m.campaignResend = map[int64][]string{}
			}
			m.campaignResend[id] = slices.Clone(reasons)
			return true, nil
		}
	}
	return false, nil
}

// TakeResend devolve e esvazia o pedido de reenvio.
func (m *Memory) TakeResend(ctx context.Context, id int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.campaignResend[id]
	delete(m.campaignResend, id)
	return r, nil
}

// ArchiveCampaigns arquiva ou desarquiva as encerradas.
func (m *Memory) ArchiveCampaigns(ctx context.Context, ids []int64, archive bool, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.campaigns {
		c := &m.campaigns[i]
		if !slices.Contains(ids, c.ID) {
			continue
		}
		switch {
		case archive && c.Finished() && c.ArchivedAt == nil:
			t := at.UTC()
			c.ArchivedAt = &t
			n++
		case !archive && c.ArchivedAt != nil:
			c.ArchivedAt = nil
			n++
		}
	}
	return n, nil
}

// DeleteCampaign exclui a arquivada; os jobs ficam sem a ligação.
func (m *Memory) DeleteCampaign(ctx context.Context, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.campaigns, func(c campaign.Campaign) bool { return c.ID == id })
	if i < 0 || m.campaigns[i].ArchivedAt == nil || !m.campaigns[i].Finished() {
		return false, nil
	}
	for _, j := range m.jobs {
		if j.campaignID == id {
			j.campaignID, j.campaignIdx = 0, 0
		}
	}
	m.campaigns = slices.Delete(m.campaigns, i, i+1)
	return true, nil
}

// CampaignJobs lista os jobs da campanha num grupo de implantação.
func (m *Memory) CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []campaign.JobState
	for _, j := range m.jobs {
		if j.campaignID == id && j.campaignIdx == idx {
			out = append(out, campaign.JobState{ID: j.ID, MachineID: j.MachineID, State: j.State, Detail: j.Detail, FinishedAt: j.FinishedAt,
				Slot: jobSlot(j.Type, j.signed.Payload), Unblocked: j.unblockedAt})
		}
	}
	return out, nil
}

// MarkJobsUnblocked na memória.
func (m *Memory) MarkJobsUnblocked(ctx context.Context, machineID string, ids []int64, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if j.MachineID == machineID && slices.Contains(ids, j.ID) && j.State == "adiado" && j.unblockedAt == nil {
			t := at
			j.unblockedAt = &t
			n++
		}
	}
	return n, nil
}

// SetMachineCapabilities na memória.
func (m *Memory) SetMachineCapabilities(ctx context.Context, id string, caps []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.machines[id]; mm != nil {
		mm.capabilities = slices.Clone(caps)
	}
	return nil
}

// MachineCapabilities na memória.
func (m *Memory) MachineCapabilities(ctx context.Context, id string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mm := m.machines[id]; mm != nil {
		return slices.Clone(mm.capabilities), nil
	}
	return nil, nil
}

// DismissJob: adiado → dispensado.
func (m *Memory) DismissJob(ctx context.Context, id int64, why string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id && slices.Contains([]string{"adiado", "falhou", "expirado", "rejeitado"}, j.State) {
			j.State, j.Detail = "dispensado", j.Detail+"; "+why
			return true, nil
		}
	}
	return false, nil
}

// jobSlot: o que distingue os jobs de uma mesma máquina numa campanha (o programa, no de
// terceiros); vazio nos outros tipos.
func jobSlot(typ, payload string) string {
	if typ != jobs.TypeThirdParty {
		return ""
	}
	var j struct {
		Params struct {
			App string `json:"app"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(payload), &j) != nil {
		return ""
	}
	return j.Params.App
}
