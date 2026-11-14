package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Desempenho e alertas (docs/monitoramento.md, seção 5).

// PerfHour é a média e o pico de uma hora.
type PerfHour struct {
	Hour           time.Time
	CPUAvg, CPUMax float64
	MemAvg, MemMax float64
	DiskMin        *float64 // o menor % livre entre os volumes; nil: sem volume
	Samples        int
}

// PerfAlert é um alerta de desempenho.
type PerfAlert struct {
	ID                    int64
	MachineID             string
	Metric, Target        string
	OpenedAt              time.Time
	ClosedAt              *time.Time
	OpenValue, Peak, Last float64
	AckBy, AckNote        string
	AckAt                 *time.Time
	// CloseBy: quem fechou à mão (vazio: fechou sozinho, com a medida de volta ao normal).
	CloseBy, CloseNote string
}

// PerfAlertFilter escolhe os alertas.
type PerfAlertFilter struct {
	MachineID string // "" = todas
	OpenOnly  bool
	Limit     int
}

// newSamples: as amostras depois da última que a regra já viu (um lote reenviado depois de
// uma falha não conta duas vezes).
func newSamples(st perfeval.State, samples []protocol.PerfSample) []protocol.PerfSample {
	var last time.Time
	for _, t := range st {
		if t.At.After(last) {
			last = t.At
		}
	}
	out := make([]protocol.PerfSample, 0, len(samples))
	for _, s := range samples {
		if s.At.After(last) {
			out = append(out, s)
		}
	}
	return out
}

// SavePerf grava as amostras, passa as novas pela regra e abre ou fecha os alertas, tudo numa
// transação (dois check-ins da mesma máquina não contam a mesma amostra duas vezes).
func (p *Postgres) SavePerf(ctx context.Context, machineID string, samples []protocol.PerfSample, policy protocol.MonitorPolicy,
	now time.Time) ([]perfeval.Change, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A linha do estado da regra trava a máquina (dois check-ins dela não contam a mesma
	// amostra duas vezes). Ela quase sempre já existe: criar antes, a cada check-in, era uma
	// ida ao banco para nada.
	const lock = `SELECT state FROM perf_eval WHERE machine_id = $1 FOR UPDATE`
	var raw []byte
	err = tx.QueryRow(ctx, lock, machineID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(ctx, `INSERT INTO perf_eval (machine_id, state, updated_at) VALUES ($1, '{}', $2) ON CONFLICT DO NOTHING`,
			machineID, now); err == nil {
			err = tx.QueryRow(ctx, lock, machineID).Scan(&raw)
		}
	}
	if err != nil {
		return nil, err
	}
	st := perfeval.State{}
	if err := json.Unmarshal(raw, &st); err != nil {
		st = perfeval.State{} // estado ilegível: recomeça a contagem
	}
	ats := make([]time.Time, len(samples))
	cpus := make([]float64, len(samples))
	mems := make([]float64, len(samples))
	disks := make([]string, len(samples))
	for i, s := range samples {
		d, _ := json.Marshal(s.Disks)
		if s.Disks == nil {
			d = []byte("{}")
		}
		ats[i], cpus[i], mems[i], disks[i] = s.At.UTC(), s.CPU, s.Mem, string(d)
	}
	// As gravações vão juntas, num envio só (eram de quatro a mais idas ao banco, com a
	// conexão do pool presa entre uma e outra): as amostras, a marca de hora atrasada, os
	// alertas que a regra abriu ou fechou e o estado novo da regra.
	w := &pgx.Batch{}
	w.Queue(`INSERT INTO perf_samples (machine_id, at, cpu, mem, disks)
		SELECT $1, t, c, m, d::jsonb FROM unnest($2::timestamptz[], $3::float8[], $4::float8[], $5::text[]) AS u(t, c, m, d)
		ON CONFLICT DO NOTHING`, machineID, ats, cpus, mems, disks)
	// Amostra de uma hora que a passada de hora em hora já montou: a hora fica marcada para
	// ser refeita (RollupLatePerf), em vez de a passada refazer 48 horas do parque inteiro.
	if late := now.UTC().Truncate(time.Hour).Add(-PerfLateAfter); slices.ContainsFunc(ats, func(t time.Time) bool { return t.Before(late) }) {
		w.Queue(`INSERT INTO perf_late (machine_id, hour)
			SELECT DISTINCT $1, date_trunc('hour', t AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
			FROM unnest($2::timestamptz[]) AS u(t) WHERE t < $3
			ON CONFLICT DO NOTHING`, machineID, ats, late)
	}
	var changes []perfeval.Change
	for _, s := range newSamples(st, samples) {
		for _, c := range st.Step(policy, s) {
			changes = append(changes, c)
			if c.Opened {
				w.Queue(`INSERT INTO perf_alerts (machine_id, metric, target, opened_at, open_value, peak, last_value)
					VALUES ($1, $2, $3, $4, $5, $5, $5)
					ON CONFLICT (machine_id, metric, target) WHERE closed_at IS NULL DO NOTHING`,
					machineID, c.Key.Metric, c.Key.Target, c.At, c.Value)
			} else {
				w.Queue(`UPDATE perf_alerts SET closed_at = $4, peak = $5, last_value = $6
					WHERE machine_id = $1 AND metric = $2 AND target = $3 AND closed_at IS NULL`,
					machineID, c.Key.Metric, c.Key.Target, c.At, c.Peak, c.Value)
			}
		}
	}
	for id, t := range st {
		if !t.Open {
			continue
		}
		k := perfeval.KeyOf(id)
		w.Queue(`UPDATE perf_alerts SET peak = $4, last_value = $5
			WHERE machine_id = $1 AND metric = $2 AND target = $3 AND closed_at IS NULL`,
			machineID, k.Metric, k.Target, t.Peak, t.Last)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	w.Queue(`UPDATE perf_eval SET state = $2, updated_at = $3 WHERE machine_id = $1`, machineID, b, now)
	res := tx.SendBatch(ctx, w)
	for range w.Len() {
		if _, err = res.Exec(); err != nil {
			break
		}
	}
	if cerr := res.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return changes, tx.Commit(ctx)
}

// PerfSamples: as amostras da máquina desde since, em ordem.
func (p *Postgres) PerfSamples(ctx context.Context, machineID string, since time.Time) ([]protocol.PerfSample, error) {
	rows, err := p.pool.Query(ctx, `SELECT at, cpu, mem, disks FROM perf_samples WHERE machine_id = $1 AND at >= $2 ORDER BY at`,
		machineID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.PerfSample
	for rows.Next() {
		var s protocol.PerfSample
		var cpu, mem float32
		var d []byte
		if err := rows.Scan(&s.At, &cpu, &mem, &d); err != nil {
			return nil, err
		}
		s.CPU, s.Mem = float64(cpu), float64(mem)
		if err := json.Unmarshal(d, &s.Disks); err != nil || len(s.Disks) == 0 {
			s.Disks = nil
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PerfHourly: as horas da máquina desde since, em ordem.
func (p *Postgres) PerfHourly(ctx context.Context, machineID string, since time.Time) ([]PerfHour, error) {
	rows, err := p.pool.Query(ctx, `SELECT hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_min, samples
		FROM perf_hourly WHERE machine_id = $1 AND hour >= $2 ORDER BY hour`, machineID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PerfHour
	for rows.Next() {
		var h PerfHour
		var ca, cm, ma, mm float32
		var dm *float32
		if err := rows.Scan(&h.Hour, &ca, &cm, &ma, &mm, &dm, &h.Samples); err != nil {
			return nil, err
		}
		h.CPUAvg, h.CPUMax, h.MemAvg, h.MemMax = float64(ca), float64(cm), float64(ma), float64(mm)
		if dm != nil {
			v := float64(*dm)
			h.DiskMin = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

const perfAlertCols = `id, machine_id, metric, target, opened_at, closed_at, open_value, peak, last_value, ack_by, ack_at, ack_note,
	closed_by, close_note`

func scanPerfAlert(row pgx.Row) (PerfAlert, error) {
	var a PerfAlert
	var ov, pk, lv float32
	err := row.Scan(&a.ID, &a.MachineID, &a.Metric, &a.Target, &a.OpenedAt, &a.ClosedAt, &ov, &pk, &lv, &a.AckBy, &a.AckAt, &a.AckNote, &a.CloseBy, &a.CloseNote)
	a.OpenValue, a.Peak, a.Last = float64(ov), float64(pk), float64(lv)
	return a, err
}

// PerfAlerts: os alertas, os abertos primeiro, do mais novo para o mais velho.
func (p *Postgres) PerfAlerts(ctx context.Context, f PerfAlertFilter) ([]PerfAlert, error) {
	if f.Limit <= 0 {
		f.Limit = 200
	}
	rows, err := p.pool.Query(ctx, `SELECT `+perfAlertCols+` FROM perf_alerts
		WHERE ($1 = '' OR machine_id = $1) AND (NOT $2 OR closed_at IS NULL)
		ORDER BY (closed_at IS NULL) DESC, opened_at DESC, id DESC LIMIT $3`, f.MachineID, f.OpenOnly, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PerfAlert
	for rows.Next() {
		a, err := scanPerfAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckPerfAlert reconhece um alerta aberto ainda não reconhecido.
func (p *Postgres) AckPerfAlert(ctx context.Context, id int64, by, note string, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE perf_alerts SET ack_by = $2, ack_note = $3, ack_at = $4
		WHERE id = $1 AND closed_at IS NULL AND ack_at IS NULL`, id, by, note, at)
	return tag.RowsAffected() > 0, err
}

// AckPerfAlerts reconhece, em lote, os alertas abertos e ainda não reconhecidos entre os ids.
// Devolve quantos mudaram.
func (p *Postgres) AckPerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE perf_alerts SET ack_by = $2, ack_note = $3, ack_at = $4
		WHERE id = ANY($1) AND closed_at IS NULL AND ack_at IS NULL`, ids, by, note, at)
	return int(tag.RowsAffected()), err
}

// ClosePerfAlerts fecha à mão, em lote, os alertas ainda abertos entre os ids, e zera a
// contagem da regra de cada um: se a medida continuar além do limite, um alerta novo abre
// depois dos minutos seguidos. Devolve quantos fecharam.
func (p *Postgres) ClosePerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE perf_alerts SET closed_at = $2, closed_by = $3, close_note = $4
		WHERE id = ANY($1) AND closed_at IS NULL RETURNING machine_id, metric, target`, ids, at, by, note)
	if err != nil {
		return 0, err
	}
	byMachine := map[string][]perfeval.Key{}
	n := 0
	for rows.Next() {
		var m string
		var k perfeval.Key
		if err := rows.Scan(&m, &k.Metric, &k.Target); err != nil {
			rows.Close()
			return 0, err
		}
		byMachine[m] = append(byMachine[m], k)
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	machines := make([]string, 0, len(byMachine))
	for m := range byMachine {
		machines = append(machines, m)
	}
	sort.Strings(machines) // a mesma ordem de trava que outra transação usaria
	for _, m := range machines {
		var raw []byte
		switch err := tx.QueryRow(ctx, `SELECT state FROM perf_eval WHERE machine_id = $1 FOR UPDATE`, m).Scan(&raw); {
		case errors.Is(err, pgx.ErrNoRows):
			continue
		case err != nil:
			return 0, err
		}
		st := perfeval.State{}
		if err := json.Unmarshal(raw, &st); err != nil {
			continue // ilegível: o SavePerf recomeça a contagem de qualquer jeito
		}
		for _, k := range byMachine[m] {
			st.Reset(k)
		}
		b, err := json.Marshal(st)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE perf_eval SET state = $2 WHERE machine_id = $1`, m, b); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit(ctx)
}

// PerfAlertCounts: os alertas abertos e, entre eles, os ainda não reconhecidos.
func (p *Postgres) PerfAlertCounts(ctx context.Context) (open, unacked int, err error) {
	err = p.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE ack_at IS NULL) FROM perf_alerts WHERE closed_at IS NULL`).
		Scan(&open, &unacked)
	return open, unacked, err
}

// RollupPerf monta as horas de [from, to) a partir das amostras (refaz as que já existiam:
// uma amostra que chegou atrasada entra na conta).
func (p *Postgres) RollupPerf(ctx context.Context, from, to time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO perf_hourly (machine_id, hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_min, samples)
		SELECT machine_id, date_trunc('hour', at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			avg(cpu), max(cpu), avg(mem), max(mem),
			min((SELECT min(value::real) FROM jsonb_each_text(disks))), count(*)
		FROM perf_samples WHERE at >= $1 AND at < $2
		GROUP BY 1, 2
		ON CONFLICT (machine_id, hour) DO UPDATE SET cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max,
			mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max, disk_min = EXCLUDED.disk_min, samples = EXCLUDED.samples`,
		from, to)
	return tag.RowsAffected(), err
}

// PerfLateAfter: a amostra que chega com a hora dela mais velha que isso (contado do começo
// da hora atual) é "atrasada": a passada regular, que refaz PerfLateAfter mais uma hora, pode
// já não alcançá-la, e a hora dela vai para perf_late.
const PerfLateAfter = 2 * time.Hour

// RollupLatePerf refaz as horas marcadas por amostras atrasadas e tira a marca, num comando
// só: a marca que chegar durante a conta fica para a próxima passada.
func (p *Postgres) RollupLatePerf(ctx context.Context) (int64, error) {
	tag, err := p.pool.Exec(ctx, `WITH l AS (DELETE FROM perf_late RETURNING machine_id, hour)
		INSERT INTO perf_hourly (machine_id, hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_min, samples)
		SELECT s.machine_id, l.hour, avg(s.cpu), max(s.cpu), avg(s.mem), max(s.mem),
			min((SELECT min(value::real) FROM jsonb_each_text(s.disks))), count(*)
		FROM l JOIN perf_samples s ON s.machine_id = l.machine_id AND s.at >= l.hour AND s.at < l.hour + interval '1 hour'
		GROUP BY 1, 2
		ON CONFLICT (machine_id, hour) DO UPDATE SET cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max,
			mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max, disk_min = EXCLUDED.disk_min, samples = EXCLUDED.samples`)
	return tag.RowsAffected(), err
}

// RollupLatePerf na memória.
func (m *Memory) RollupLatePerf(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.perf()
	var n int64
	for id, hours := range d.late {
		for h := range hours {
			n += m.rollupPerf(h, h.Add(time.Hour), id)
		}
	}
	d.late = nil
	return n, nil
}

// PerfLastHour: a hora mais nova já montada (zero sem nenhuma). A passada da partida começa
// dela, em vez de refazer as 48 horas a cada reinício.
func (p *Postgres) PerfLastHour(ctx context.Context) (time.Time, error) {
	var last *time.Time
	if err := p.pool.QueryRow(ctx, `SELECT max(hour) FROM perf_hourly`).Scan(&last); err != nil || last == nil {
		return time.Time{}, err
	}
	return last.UTC(), nil
}

// PerfLastHour na memória.
func (m *Memory) PerfLastHour(ctx context.Context) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var last time.Time
	if m.perfData == nil {
		return last, nil
	}
	for _, list := range m.perfData.hours {
		for _, h := range list {
			if h.Hour.After(last) {
				last = h.Hour
			}
		}
	}
	return last, nil
}

// PrunePerf apaga as amostras, as horas e os alertas fechados mais velhos que os cortes. Um
// corte zero pula a tabela dele (as horas são limpas uma vez por dia, não a cada passada).
func (p *Postgres) PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error) {
	var total int64
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM perf_samples WHERE at < $1`, samplesBefore},
		{`DELETE FROM perf_hourly WHERE hour < $1`, hoursBefore},
		{`DELETE FROM perf_alerts WHERE closed_at IS NOT NULL AND closed_at < $1`, alertsBefore},
	} {
		if q.at.IsZero() {
			continue
		}
		tag, err := p.pool.Exec(ctx, q.sql, q.at)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// --- Memória ------------------------------------------------------------------------

type memPerf struct {
	samples map[string][]protocol.PerfSample // por máquina, em ordem
	hours   map[string][]PerfHour
	eval    map[string]perfeval.State
	alerts  []PerfAlert                   // ID = posição + 1
	late    map[string]map[time.Time]bool // horas com amostra atrasada, por máquina
}

func (m *Memory) perf() *memPerf {
	if m.perfData == nil {
		m.perfData = &memPerf{samples: map[string][]protocol.PerfSample{}, hours: map[string][]PerfHour{}, eval: map[string]perfeval.State{}}
	}
	return m.perfData
}

// SavePerf na memória.
func (m *Memory) SavePerf(ctx context.Context, machineID string, samples []protocol.PerfSample, policy protocol.MonitorPolicy,
	now time.Time) ([]perfeval.Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.machines[machineID]; !ok {
		return nil, errors.New("máquina desconhecida")
	}
	d := m.perf()
	list := d.samples[machineID]
	for _, s := range samples {
		if !slices.ContainsFunc(list, func(x protocol.PerfSample) bool { return x.At.Equal(s.At) }) {
			list = append(list, s)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	d.samples[machineID] = list
	for _, s := range samples {
		if s.At.Before(now.UTC().Truncate(time.Hour).Add(-PerfLateAfter)) {
			if d.late == nil {
				d.late = map[string]map[time.Time]bool{}
			}
			if d.late[machineID] == nil {
				d.late[machineID] = map[time.Time]bool{}
			}
			d.late[machineID][s.At.UTC().Truncate(time.Hour)] = true
		}
	}
	st := d.eval[machineID]
	if st == nil {
		st = perfeval.State{}
		d.eval[machineID] = st
	}
	open := func(k perfeval.Key) *PerfAlert {
		for i := range d.alerts {
			if a := &d.alerts[i]; a.MachineID == machineID && a.Metric == k.Metric && a.Target == k.Target && a.ClosedAt == nil {
				return a
			}
		}
		return nil
	}
	var changes []perfeval.Change
	for _, s := range newSamples(st, samples) {
		for _, c := range st.Step(policy, s) {
			changes = append(changes, c)
			switch a := open(c.Key); {
			case c.Opened && a == nil:
				d.alerts = append(d.alerts, PerfAlert{ID: int64(len(d.alerts) + 1), MachineID: machineID, Metric: c.Key.Metric,
					Target: c.Key.Target, OpenedAt: c.At, OpenValue: c.Value, Peak: c.Value, Last: c.Value})
			case !c.Opened && a != nil:
				at := c.At
				a.ClosedAt, a.Peak, a.Last = &at, c.Peak, c.Value
			}
		}
	}
	for id, t := range st {
		if a := open(perfeval.KeyOf(id)); t.Open && a != nil {
			a.Peak, a.Last = t.Peak, t.Last
		}
	}
	return changes, nil
}

// PerfSamples na memória.
func (m *Memory) PerfSamples(ctx context.Context, machineID string, since time.Time) ([]protocol.PerfSample, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []protocol.PerfSample
	for _, s := range m.perf().samples[machineID] {
		if !s.At.Before(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

// PerfHourly na memória.
func (m *Memory) PerfHourly(ctx context.Context, machineID string, since time.Time) ([]PerfHour, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []PerfHour
	for _, h := range m.perf().hours[machineID] {
		if !h.Hour.Before(since) {
			out = append(out, h)
		}
	}
	return out, nil
}

// PerfAlerts na memória.
func (m *Memory) PerfAlerts(ctx context.Context, f PerfAlertFilter) ([]PerfAlert, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if f.Limit <= 0 {
		f.Limit = 200
	}
	var out []PerfAlert
	for _, a := range m.perf().alerts {
		if a.MachineID != "" && (f.MachineID == "" || a.MachineID == f.MachineID) && (!f.OpenOnly || a.ClosedAt == nil) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].ClosedAt == nil) != (out[j].ClosedAt == nil) {
			return out[i].ClosedAt == nil
		}
		if !out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].OpenedAt.After(out[j].OpenedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// AckPerfAlert na memória.
func (m *Memory) AckPerfAlert(ctx context.Context, id int64, by, note string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.perf()
	if id < 1 || id > int64(len(d.alerts)) {
		return false, nil
	}
	a := &d.alerts[id-1]
	if a.ClosedAt != nil || a.AckAt != nil {
		return false, nil
	}
	a.AckBy, a.AckNote, a.AckAt = by, note, &at
	return true, nil
}

// AckPerfAlerts na memória.
func (m *Memory) AckPerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, n := m.perf(), 0
	for i := range d.alerts {
		if a := &d.alerts[i]; a.MachineID != "" && slices.Contains(ids, a.ID) && a.ClosedAt == nil && a.AckAt == nil {
			a.AckBy, a.AckNote, a.AckAt = by, note, &at
			n++
		}
	}
	return n, nil
}

// ClosePerfAlerts na memória.
func (m *Memory) ClosePerfAlerts(ctx context.Context, ids []int64, by, note string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, n := m.perf(), 0
	for i := range d.alerts {
		if a := &d.alerts[i]; a.MachineID != "" && slices.Contains(ids, a.ID) && a.ClosedAt == nil {
			a.ClosedAt, a.CloseBy, a.CloseNote = &at, by, note
			if st := d.eval[a.MachineID]; st != nil {
				st.Reset(perfeval.Key{Metric: a.Metric, Target: a.Target})
			}
			n++
		}
	}
	return n, nil
}

// PerfAlertCounts na memória.
func (m *Memory) PerfAlertCounts(ctx context.Context) (open, unacked int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.perf().alerts {
		if a.ClosedAt == nil && a.MachineID != "" { // MachineID vazio: apagado pela retenção
			open++
			if a.AckAt == nil {
				unacked++
			}
		}
	}
	return open, unacked, nil
}

// RollupPerf na memória.
func (m *Memory) RollupPerf(ctx context.Context, from, to time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rollupPerf(from, to, ""), nil
}

// rollupPerf monta as horas de [from, to), de todas as máquinas ou só de uma (com m.mu).
func (m *Memory) rollupPerf(from, to time.Time, only string) int64 {
	d := m.perf()
	var n int64
	for id, list := range d.samples {
		if only != "" && id != only {
			continue
		}
		byHour := map[time.Time][]protocol.PerfSample{}
		for _, s := range list {
			if !s.At.Before(from) && s.At.Before(to) {
				h := s.At.UTC().Truncate(time.Hour)
				byHour[h] = append(byHour[h], s)
			}
		}
		for h, ss := range byHour {
			ph := PerfHour{Hour: h, Samples: len(ss)}
			for _, s := range ss {
				ph.CPUAvg += s.CPU / float64(len(ss))
				ph.MemAvg += s.Mem / float64(len(ss))
				ph.CPUMax, ph.MemMax = max(ph.CPUMax, s.CPU), max(ph.MemMax, s.Mem)
				for _, v := range s.Disks {
					if ph.DiskMin == nil || v < *ph.DiskMin {
						vv := v
						ph.DiskMin = &vv
					}
				}
			}
			hours := slices.DeleteFunc(d.hours[id], func(x PerfHour) bool { return x.Hour.Equal(h) })
			hours = append(hours, ph)
			sort.Slice(hours, func(i, j int) bool { return hours[i].Hour.Before(hours[j].Hour) })
			d.hours[id] = hours
			n++
		}
	}
	return n
}

// PrunePerf na memória (os alertas apagados ficam como buracos de ID: o ID é a posição).
func (m *Memory) PrunePerf(ctx context.Context, samplesBefore, hoursBefore, alertsBefore time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.perf()
	var n int64
	for id, list := range d.samples {
		before := len(list)
		d.samples[id] = slices.DeleteFunc(list, func(s protocol.PerfSample) bool { return s.At.Before(samplesBefore) })
		n += int64(before - len(d.samples[id]))
	}
	for id, list := range d.hours {
		before := len(list)
		d.hours[id] = slices.DeleteFunc(list, func(h PerfHour) bool { return h.Hour.Before(hoursBefore) })
		n += int64(before - len(d.hours[id]))
	}
	for i := range d.alerts {
		if a := &d.alerts[i]; a.ClosedAt != nil && a.ClosedAt.Before(alertsBefore) && a.MachineID != "" {
			*a = PerfAlert{ID: a.ID}
			n++
		}
	}
	return n, nil
}
