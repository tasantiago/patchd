package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var (
	ErrMergeInvalid = errors.New("origem ou destino inválido, já incorporado ou destino aposentado")
	ErrMergeBusy    = errors.New("o registro de origem tem jobs entregues sem resultado; aguarde a conclusão antes de mesclar")
)

type MachineMerge struct {
	SourceID, TargetID string
	At                 time.Time
	By, Reason         string
}

// MachineMergeMembers returns the current identity followed by its archived identities.
// No encrypted or signed payload is rewritten.
func (p *Postgres) MachineMergeMembers(ctx context.Context, id string) ([]MachineMerge, error) {
	rows, err := p.pool.Query(ctx, `WITH root AS (
 SELECT coalesce((SELECT target_id FROM machine_merges WHERE source_id=$1),$1) AS id)
 SELECT m.id,m.id,NULL::timestamptz,'','' FROM machines m,root r WHERE m.id=r.id
 UNION ALL
 SELECT source_id,target_id,merged_at,merged_by,reason FROM machine_merges,root WHERE target_id=root.id OR $1=''`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineMerge
	for rows.Next() {
		var m MachineMerge
		var at *time.Time
		if err := rows.Scan(&m.SourceID, &m.TargetID, &at, &m.By, &m.Reason); err != nil {
			return nil, err
		}
		if at != nil {
			m.At = at.UTC()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MergeMachines commits the archive, credential revocation, group membership and audit
// together. History stays in its original tables, including secrets bound to the old ID.
func (p *Postgres) MergeMachines(ctx context.Context, source, target, reason string, event AuditEvent) error {
	if !validMerge(source, target, reason, event) {
		return ErrMergeInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Administrative merges are rare. Serialize group edits to avoid cycles and lost members.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(734802148)"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT id,retired_at IS NOT NULL FROM machines WHERE id=ANY($1::text[]) ORDER BY id FOR UPDATE", []string{source, target})
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var id string
		var retired bool
		if err := rows.Scan(&id, &retired); err != nil {
			rows.Close()
			return err
		}
		found[id] = retired
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(found) != 2 || found[target] {
		return ErrMergeInvalid
	}
	var blocked bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM machine_merges WHERE source_id=ANY($1::text[]))", []string{source, target}).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrMergeInvalid
	}
	// Lock queued rows before checking delivered work: delivery uses SKIP LOCKED.
	locked, err := tx.Query(ctx, "SELECT id FROM jobs WHERE machine_id=$1 AND state IN ('pendente','entregue') FOR UPDATE", source)
	if err != nil {
		return err
	}
	for locked.Next() {
		var id int64
		if err := locked.Scan(&id); err != nil {
			locked.Close()
			return err
		}
	}
	err = locked.Err()
	locked.Close()
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE machine_id=$1 AND state='entregue')", source).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrMergeBusy
	}
	at := event.At
	if _, err := tx.Exec(ctx, `UPDATE machines SET retired_at=$2,retired_reason=$3,retired_by=$4,credential_hash=NULL WHERE id=$1`, source, at, "Incorporada em "+target+": "+reason, event.Username); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET state='cancelado',finished_at=$2,detail=$3 WHERE machine_id=$1 AND state='pendente'`, source, at, "Mesclagem administrativa em "+target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE selfservice_requests SET state='expirado',decided_at=$2,decided_by=$3,note=$4 WHERE machine_id=$1 AND state='aguardando'`, source, at, event.Username, "Registro incorporado em "+target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE machine_merges SET target_id=$2 WHERE target_id=$1", source, target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO machine_merges(source_id,target_id,merged_at,merged_by,reason) VALUES($1,$2,$3,$4,$5)", source, target, at, event.Username, reason); err != nil {
		return err
	}
	event.Action, event.Target, event.Detail = "maquina.mesclar", target, fmt.Sprintf("origem %s; destino %s; motivo: %s", source, target, reason)
	event.clip()
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(at,username,source,action,target,detail,remote,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, event.At, event.Username, event.Source, event.Action, event.Target, event.Detail, event.Remote, event.RequestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validMerge(source, target, reason string, e AuditEvent) bool {
	return source != "" && target != "" && source != target && len(reason) >= 10 && len(reason) <= 500 && !strings.ContainsAny(reason, "\x00\r\n") && e.Username != "" && !e.At.IsZero()
}

func (m *Memory) MachineMergeMembers(ctx context.Context, id string) ([]MachineMerge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id == "" {
		var out []MachineMerge
		for _, link := range m.merges {
			out = append(out, link)
		}
		return out, nil
	}
	if link, ok := m.merges[id]; ok {
		id = link.TargetID
	}
	if m.machines[id] == nil {
		return nil, nil
	}
	out := []MachineMerge{{SourceID: id, TargetID: id}}
	for _, link := range m.merges {
		if link.TargetID == id {
			out = append(out, link)
		}
	}
	slices.SortFunc(out[1:], func(a, b MachineMerge) int { return strings.Compare(a.SourceID, b.SourceID) })
	return out, nil
}

func (m *Memory) MergeMachines(ctx context.Context, source, target, reason string, e AuditEvent) error {
	if !validMerge(source, target, reason, e) {
		return ErrMergeInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src, dst := m.machines[source], m.machines[target]
	if src == nil || dst == nil || !dst.retiredAt.IsZero() {
		return ErrMergeInvalid
	}
	if _, ok := m.merges[source]; ok {
		return ErrMergeInvalid
	}
	if _, ok := m.merges[target]; ok {
		return ErrMergeInvalid
	}
	for _, j := range m.jobs {
		if j.MachineID == source && j.State == "entregue" {
			return ErrMergeBusy
		}
	}
	if m.merges == nil {
		m.merges = map[string]MachineMerge{}
	}
	src.retiredAt, src.retiredReason, src.retiredBy = e.At, "Incorporada em "+target+": "+reason, e.Username
	for hash, id := range m.credentials {
		if id == source {
			delete(m.credentials, hash)
		}
	}
	for _, j := range m.jobs {
		if j.MachineID == source && j.State == "pendente" {
			j.State = "cancelado"
			j.FinishedAt = &e.At
			j.Detail = "Mesclagem administrativa em " + target
		}
	}
	for i := range m.ssRequests {
		r := &m.ssRequests[i]
		if r.MachineID == source && r.State == "aguardando" {
			r.State = "expirado"
			r.DecidedAt = &e.At
			r.DecidedBy = e.Username
			r.Note = "Registro incorporado em " + target
		}
	}
	for id, link := range m.merges {
		if link.TargetID == source {
			link.TargetID = target
			m.merges[id] = link
		}
	}
	m.merges[source] = MachineMerge{SourceID: source, TargetID: target, At: e.At, By: e.Username, Reason: reason}
	e.Action, e.Target, e.Detail = "maquina.mesclar", target, fmt.Sprintf("origem %s; destino %s; motivo: %s", source, target, reason)
	e.clip()
	m.auditSeq++
	e.ID = m.auditSeq
	m.audit = append(m.audit, e)
	return nil
}
