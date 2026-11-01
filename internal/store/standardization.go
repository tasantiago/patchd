package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

const StandardizationAuthor = "padronizacao"

var ErrStandardizationConflict = errors.New("o grupo foi alterado ou excluído; recarregue antes de salvar")
var ErrStandardizationStale = errors.New("plano mudou; consulte novamente")

// StandardInstaller fixa os bytes e os parâmetros aprovados pelo administrador ao salvar o grupo.
type StandardInstaller struct {
	BundleID     int64
	OS, Software string
	Params       jobs.ScriptParams
}

func StandardScriptKey(p jobs.ScriptParams) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return "script-" + hex.EncodeToString(sum[:])
}

func (i StandardInstaller) Validate() error {
	if i.BundleID <= 0 || (i.OS != "windows" && i.OS != "linux" && i.OS != "darwin") || strings.TrimSpace(i.Software) != i.Software || i.Software == "" || len(i.Software) > 200 || strings.ContainsAny(i.Software, "\r\n\x00") {
		return errors.New("instalador exige sistema, pacote e nome exato no inventário")
	}
	if err := i.Params.Validate(); err != nil {
		return err
	}
	if (i.OS == "windows") != (i.Params.OS() == "windows") {
		return errors.New("script incompatível com o sistema selecionado")
	}
	return nil
}

var standardAppID = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}$`)
var standardMachineID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type StandardizationPolicy struct {
	ID, Revision             int64
	Name, Description        string
	Enabled, AllMachines     bool
	Machines, ADGroups, Apps []string
	Shortcuts                []protocol.StandardShortcut
	Installers               []StandardInstaller
	UpdatedBy                string
	UpdatedAt                time.Time
}

func (p StandardizationPolicy) Validate() error {
	if p.ID < 0 || p.Revision < 0 || strings.TrimSpace(p.Name) != p.Name || len([]rune(p.Name)) < 2 || len([]rune(p.Name)) > 80 || len([]rune(p.Description)) > 500 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return errors.New("nome ou descrição do grupo fora dos limites")
	}
	if len(p.Machines) > 10000 || len(p.ADGroups) > 20 || len(p.Apps)+len(p.Installers) > protocol.MaxStandardizationApps || len(p.Shortcuts) > protocol.MaxStandardizationShortcuts {
		return errors.New("grupo excede o limite de computadores, grupos AD, aplicativos ou atalhos")
	}
	if len(p.Apps)+len(p.Installers)+len(p.Shortcuts) == 0 {
		return errors.New("selecione ao menos um aplicativo ou atalho")
	}
	if p.AllMachines && len(p.Machines)+len(p.ADGroups) != 0 {
		return errors.New("o padrão básico vale para todos; remova os destinos específicos")
	}
	if p.Enabled && !p.AllMachines && len(p.Machines)+len(p.ADGroups) == 0 {
		return errors.New("selecione os computadores ou grupos do AD antes de ativar")
	}
	for _, id := range p.Machines {
		if !standardMachineID.MatchString(id) {
			return errors.New("computador inválido")
		}
	}
	for _, group := range p.ADGroups {
		if !ValidGroup(group) {
			return errors.New("grupo do AD inválido")
		}
	}
	for _, app := range p.Apps {
		if !standardAppID.MatchString(app) {
			return errors.New("aplicativo inválido")
		}
	}
	seen := map[string]bool{}
	for _, i := range p.Installers {
		if err := i.Validate(); err != nil {
			return err
		}
	}
	for _, shortcut := range p.Shortcuts {
		if err := shortcut.Validate(); err != nil {
			return err
		}
		key := shortcut.OS + ":" + strings.ToLower(shortcut.Name)
		if seen[key] {
			return errors.New("atalho com nome repetido no grupo")
		}
		seen[key] = true
	}
	return nil
}

func cloneStandardPolicy(p StandardizationPolicy) StandardizationPolicy {
	p.Machines, p.ADGroups, p.Apps = slices.Clone(p.Machines), slices.Clone(p.ADGroups), slices.Clone(p.Apps)
	p.Shortcuts = slices.Clone(p.Shortcuts)
	p.Installers = slices.Clone(p.Installers)
	for i := range p.Installers {
		p.Installers[i].Params.Args = slices.Clone(p.Installers[i].Params.Args)
	}
	return p
}

type StandardizationState struct {
	MachineID, Account string
	Plan               protocol.StandardizationPlan
	ShortcutResult     *protocol.StandardizationResult
	ShortcutsAt        *time.Time
	EvaluatedAt        time.Time
}

func (p *Postgres) StandardizationPolicies(ctx context.Context) ([]StandardizationPolicy, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, document, revision, updated_by, updated_at FROM standardization_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StandardizationPolicy{}
	for rows.Next() {
		var item StandardizationPolicy
		var doc []byte
		if err := rows.Scan(&item.ID, &doc, &item.Revision, &item.UpdatedBy, &item.UpdatedAt); err != nil {
			return nil, err
		}
		id, rev, by, at := item.ID, item.Revision, item.UpdatedBy, item.UpdatedAt
		if err := json.Unmarshal(doc, &item); err != nil {
			return nil, err
		}
		item.ID, item.Revision, item.UpdatedBy, item.UpdatedAt = id, rev, by, at.UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveStandardizationPolicy(ctx context.Context, item StandardizationPolicy) (int64, error) {
	if err := item.Validate(); err != nil {
		return 0, err
	}
	b, err := json.Marshal(item)
	if err != nil {
		return 0, err
	}
	if item.ID == 0 {
		var id int64
		err = p.pool.QueryRow(ctx, `INSERT INTO standardization_policies(document, updated_by) VALUES($1,$2) RETURNING id`, b, item.UpdatedBy).Scan(&id)
		return id, err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE standardization_policies SET document=$2, revision=revision+1, updated_by=$3, updated_at=now() WHERE id=$1 AND revision=$4`, item.ID, b, item.UpdatedBy, item.Revision)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrStandardizationConflict
	}
	return item.ID, err
}

func (p *Postgres) DeleteStandardizationPolicy(ctx context.Context, id, revision int64) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM standardization_policies WHERE id=$1 AND revision=$2`, id, revision)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrStandardizationConflict
	}
	return err
}

func (p *Postgres) WithStandardizationLock(ctx context.Context, machine string, fn func(context.Context) error) (bool, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return false, err
	}
	token := hex.EncodeToString(b)
	tag, err := p.pool.Exec(ctx, `INSERT INTO standardization_leases(machine_id,token,expires_at) VALUES($1,$2,now()+interval '2 minutes')
	 ON CONFLICT(machine_id) DO UPDATE SET token=$2,expires_at=now()+interval '2 minutes' WHERE standardization_leases.expires_at<=now()`, machine, token)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.pool.Exec(c, `DELETE FROM standardization_leases WHERE machine_id=$1 AND token=$2`, machine, token)
	}()
	return true, fn(ctx)
}

// Uma linha por aplicativo e o total em aberto; sem truncar o histórico de tentativas.
func (p *Postgres) StandardizationJobs(ctx context.Context, machine string, now time.Time) (map[string]JobRow, int, error) {
	if _, err := p.pool.Exec(ctx, `UPDATE jobs SET state='expirado',finished_at=$2,detail='prazo vencido sem resultado do agente' WHERE machine_id=$1 AND ((state='pendente' AND not_after<=$2) OR (state='entregue' AND not_after<=$3))`, machine, now, now.Add(-jobs.ResultGrace)); err != nil {
		return nil, 0, err
	}
	var open int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE machine_id=$1 AND type IN ('thirdparty','script') AND state IN ('pendente','entregue')`, machine).Scan(&open); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT ON (type, CASE WHEN type='thirdparty' THEN (payload::jsonb)->'params'->>'app' ELSE ((payload::jsonb)->'params')::text END) id, state, created_at, not_after, delivered_at, finished_at, detail, payload, type
	 FROM jobs WHERE machine_id=$1 AND type IN ('thirdparty','script') AND created_by=$2 ORDER BY type, CASE WHEN type='thirdparty' THEN (payload::jsonb)->'params'->>'app' ELSE ((payload::jsonb)->'params')::text END, id DESC`, machine, StandardizationAuthor)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]JobRow{}
	for rows.Next() {
		var row JobRow
		row.MachineID, row.Type, row.CreatedBy = machine, jobs.TypeThirdParty, StandardizationAuthor
		if err := rows.Scan(&row.ID, &row.State, &row.CreatedAt, &row.NotAfter, &row.DeliveredAt, &row.FinishedAt, &row.Detail, &row.Payload, &row.Type); err != nil {
			return nil, 0, err
		}
		var payload jobs.Job
		if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
			return nil, 0, err
		}
		key, err := standardJobKey(payload)
		if err != nil {
			return nil, 0, err
		}
		out[key] = row
	}
	return out, open, rows.Err()
}

func (p *Postgres) SaveStandardizationState(ctx context.Context, s StandardizationState) error {
	b, err := json.Marshal(s.Plan)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO standardization_states(machine_id,account,plan,evaluated_at) VALUES($1,$2,$3,$4)
	 ON CONFLICT(machine_id,account) DO UPDATE SET plan=$3,evaluated_at=$4,
	 shortcut_result=CASE WHEN standardization_states.plan->>'revision'=$5 THEN standardization_states.shortcut_result ELSE NULL END,
	 shortcuts_at=CASE WHEN standardization_states.plan->>'revision'=$5 THEN standardization_states.shortcuts_at ELSE NULL END`, s.MachineID, strings.ToLower(s.Account), b, s.EvaluatedAt, s.Plan.Revision)
	return err
}

func (p *Postgres) ReportStandardization(ctx context.Context, machine, account string, result protocol.StandardizationResult, at time.Time) error {
	if err := result.Check(); err != nil {
		return err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE standardization_states SET shortcut_result=$3,shortcuts_at=$4 WHERE machine_id=$1 AND account=$2 AND plan->>'revision'=$5`, machine, strings.ToLower(account), b, at, result.Revision)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrStandardizationStale
	}
	return err
}

func (p *Postgres) StandardizationStates(ctx context.Context) ([]StandardizationState, error) {
	rows, err := p.pool.Query(ctx, `SELECT s.machine_id,s.account,s.plan,s.shortcut_result,s.shortcuts_at,s.evaluated_at FROM standardization_states s JOIN machines m ON m.id=s.machine_id WHERE m.retired_at IS NULL ORDER BY s.evaluated_at DESC,s.machine_id,s.account LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StandardizationState{}
	for rows.Next() {
		var s StandardizationState
		var plan, result []byte
		if err := rows.Scan(&s.MachineID, &s.Account, &plan, &result, &s.ShortcutsAt, &s.EvaluatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(plan, &s.Plan); err != nil {
			return nil, err
		}
		if result != nil {
			s.ShortcutResult = &protocol.StandardizationResult{}
			if err := json.Unmarshal(result, s.ShortcutResult); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Memory) StandardizationPolicies(ctx context.Context) ([]StandardizationPolicy, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []StandardizationPolicy{}
	for _, p := range m.standardPolicies {
		out = append(out, cloneStandardPolicy(p))
	}
	slices.SortFunc(out, func(a, b StandardizationPolicy) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out, nil
}

func (m *Memory) SaveStandardizationPolicy(ctx context.Context, p StandardizationPolicy) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.standardPolicies == nil {
		m.standardPolicies = map[int64]StandardizationPolicy{}
	}
	if p.ID == 0 {
		m.standardPolicySeq++
		p.ID = m.standardPolicySeq
		p.Revision = 1
	} else {
		old, ok := m.standardPolicies[p.ID]
		if !ok || old.Revision != p.Revision {
			return 0, ErrStandardizationConflict
		}
		p.Revision++
	}
	p.UpdatedAt = m.now().UTC()
	m.standardPolicies[p.ID] = cloneStandardPolicy(p)
	return p.ID, nil
}

func (m *Memory) DeleteStandardizationPolicy(ctx context.Context, id, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.standardPolicies[id]
	if !ok || p.Revision != revision {
		return ErrStandardizationConflict
	}
	delete(m.standardPolicies, id)
	return nil
}

func (m *Memory) WithStandardizationLock(ctx context.Context, machine string, fn func(context.Context) error) (bool, error) {
	m.mu.Lock()
	if m.standardLocks == nil {
		m.standardLocks = map[string]bool{}
	}
	if m.standardLocks[machine] {
		m.mu.Unlock()
		return false, nil
	}
	m.standardLocks[machine] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.standardLocks, machine); m.mu.Unlock() }()
	return true, fn(ctx)
}

func (m *Memory) StandardizationJobs(ctx context.Context, machine string, now time.Time) (map[string]JobRow, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]JobRow{}
	open := 0
	for _, j := range m.jobs {
		if j.MachineID != machine || (j.Type != jobs.TypeThirdParty && j.Type != jobs.TypeScript) {
			continue
		}
		if (j.State == jobs.StatePending && !j.NotAfter.After(now)) || (j.State == jobs.StateDelivered && !j.NotAfter.After(now.Add(-jobs.ResultGrace))) {
			j.State = jobs.StateExpired
			t := now
			j.FinishedAt = &t
		}
		if !jobs.Final(j.State) {
			open++
		}
		if j.CreatedBy != StandardizationAuthor {
			continue
		}
		var payload jobs.Job
		if err := json.Unmarshal([]byte(j.signed.Payload), &payload); err != nil {
			return nil, 0, err
		}
		key, err := standardJobKey(payload)
		if err != nil {
			return nil, 0, err
		}
		if old, ok := out[key]; !ok || j.ID > old.ID {
			row := j.JobRow
			row.Payload = j.signed.Payload
			out[key] = row
		}
	}
	return out, open, nil
}

func (m *Memory) SaveStandardizationState(ctx context.Context, s StandardizationState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.machines[s.MachineID]; mm == nil || !mm.retiredAt.IsZero() {
		return jobs.ErrMachineUnavailable
	}
	if m.standardStates == nil {
		m.standardStates = map[string]StandardizationState{}
	}
	s.Account = strings.ToLower(s.Account)
	key := s.MachineID + "\x00" + s.Account
	if old, ok := m.standardStates[key]; ok && old.Plan.Revision == s.Plan.Revision {
		s.ShortcutResult, s.ShortcutsAt = old.ShortcutResult, old.ShortcutsAt
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var copy StandardizationState
	if err := json.Unmarshal(b, &copy); err != nil {
		return err
	}
	m.standardStates[key] = copy
	return nil
}

func (m *Memory) ReportStandardization(ctx context.Context, machine, account string, result protocol.StandardizationResult, at time.Time) error {
	if err := result.Check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := machine + "\x00" + strings.ToLower(account)
	s, ok := m.standardStates[key]
	if !ok || s.Plan.Revision != result.Revision {
		return ErrStandardizationStale
	}
	result.Items = slices.Clone(result.Items)
	s.ShortcutResult = &result
	t := at.UTC()
	s.ShortcutsAt = &t
	m.standardStates[key] = s
	return nil
}

func (m *Memory) StandardizationStates(ctx context.Context) ([]StandardizationState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []StandardizationState{}
	for _, s := range m.standardStates {
		if mm := m.machines[s.MachineID]; mm != nil && mm.retiredAt.IsZero() {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b StandardizationState) int {
		if d := b.EvaluatedAt.Compare(a.EvaluatedAt); d != 0 {
			return d
		}
		return strings.Compare(a.MachineID+"\x00"+a.Account, b.MachineID+"\x00"+b.Account)
	})
	if len(out) > 500 {
		out = out[:500]
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("copiar estados: %w", err)
	}
	return out, nil
}

func standardJobKey(payload jobs.Job) (string, error) {
	if payload.Type == jobs.TypeScript {
		p, err := jobs.ParseScriptParams(payload.Params)
		return StandardScriptKey(p), err
	}
	p, err := jobs.ParseThirdPartyParams(payload.Params)
	return p.App, err
}
