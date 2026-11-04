package store

import (
	"context"
	"time"

	"github.com/tasantiago/patchd/internal/srvhealth"
)

// A saúde do servidor (docs/saude-servidor.md): o pool, as estatísticas do PostgreSQL e as
// horas (server_health_hourly, 90 dias). Só o PostgreSQL: o servidor em memória mostra só as
// 24 horas do processo.

var _ srvhealth.Keeper = (*Postgres)(nil)

// PoolStat é o retrato do pool de conexões deste processo.
func (p *Postgres) PoolStat() srvhealth.PoolStat {
	s := p.pool.Stat()
	return srvhealth.PoolStat{Max: s.MaxConns(), Total: s.TotalConns(), InUse: s.AcquiredConns(),
		Acquires: s.AcquireCount(), Waits: s.EmptyAcquireCount(), WaitTime: s.EmptyAcquireWaitTime()}
}

// healthTables: as maiores tabelas mostradas.
const healthTables = 8

// DBStats lê o que o PostgreSQL conta sobre o banco. Nada aqui exige superusuário: as
// contagens de pg_stat_activity e pg_stat_database são visíveis para qualquer papel.
func (p *Postgres) DBStats(ctx context.Context) (srvhealth.DBStats, error) {
	var st srvhealth.DBStats
	var hit, read int64
	var longest float64
	err := p.pool.QueryRow(ctx, `
		SELECT pg_database_size(current_database()),
		       (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'),
		       current_setting('max_connections')::int,
		       COALESCE(d.blks_hit, 0), COALESCE(d.blks_read, 0), COALESCE(d.deadlocks, 0), COALESCE(d.temp_bytes, 0),
		       COALESCE((SELECT EXTRACT(EPOCH FROM max(now() - query_start)) FROM pg_stat_activity
		                 WHERE state = 'active' AND pid <> pg_backend_pid() AND backend_type = 'client backend'), 0)
		FROM pg_stat_database d WHERE d.datname = current_database()`).
		Scan(&st.Bytes, &st.Connections, &st.MaxConnections, &hit, &read, &st.Deadlocks, &st.TempBytes, &longest)
	if err != nil {
		return st, err
	}
	st.BlocksRead = hit + read
	st.CacheHitPct = srvhealth.Unknown
	if hit+read > 0 {
		st.CacheHitPct = 100 * float64(hit) / float64(hit+read)
	}
	st.LongestQuery = time.Duration(longest * float64(time.Second))
	rows, err := p.pool.Query(ctx, `
		SELECT relname, pg_total_relation_size(relid) AS n
		FROM pg_stat_user_tables WHERE schemaname = current_schema()
		ORDER BY n DESC, relname LIMIT $1`, healthTables)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var t srvhealth.TableSize
		if err := rows.Scan(&t.Name, &t.Bytes); err != nil {
			return st, err
		}
		st.Tables = append(st.Tables, t)
	}
	return st, rows.Err()
}

func unknownToNull(v float64) *float64 {
	if v == srvhealth.Unknown {
		return nil
	}
	return &v
}

func nullToUnknown(v *float64) float64 {
	if v == nil {
		return srvhealth.Unknown
	}
	return *v
}

// SaveHealthHour grava a hora. Se ela já existe (o processo reiniciou no meio da hora),
// junta: as médias pesadas pelas amostras (a latência, pelas requisições), os picos pelo maior.
func (p *Postgres) SaveHealthHour(ctx context.Context, h srvhealth.Hour) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO server_health_hourly AS o (hour, samples, cpu_avg, cpu_max, host_cpu_avg, host_cpu_max, mem_avg, mem_max,
			mem_bytes_max, host_mem_max, requests, slow, errors, lat_avg_ms, lat_p95_max_ms, pool_in_use, pool_max,
			pool_waits, disk_free_min, db_bytes, goroutines)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (hour) DO UPDATE SET
			samples = o.samples + EXCLUDED.samples,
			cpu_avg = (o.cpu_avg * o.samples + EXCLUDED.cpu_avg * EXCLUDED.samples) / (o.samples + EXCLUDED.samples),
			cpu_max = GREATEST(o.cpu_max, EXCLUDED.cpu_max),
			host_cpu_avg = COALESCE((o.host_cpu_avg * o.samples + EXCLUDED.host_cpu_avg * EXCLUDED.samples) / (o.samples + EXCLUDED.samples),
			                        EXCLUDED.host_cpu_avg, o.host_cpu_avg),
			host_cpu_max = GREATEST(o.host_cpu_max, EXCLUDED.host_cpu_max),
			mem_avg = (o.mem_avg * o.samples + EXCLUDED.mem_avg * EXCLUDED.samples) / (o.samples + EXCLUDED.samples),
			mem_max = GREATEST(o.mem_max, EXCLUDED.mem_max),
			mem_bytes_max = GREATEST(o.mem_bytes_max, EXCLUDED.mem_bytes_max),
			host_mem_max = GREATEST(o.host_mem_max, EXCLUDED.host_mem_max),
			requests = o.requests + EXCLUDED.requests,
			slow = o.slow + EXCLUDED.slow,
			errors = o.errors + EXCLUDED.errors,
			lat_avg_ms = CASE WHEN o.requests + EXCLUDED.requests = 0 THEN 0
			             ELSE (o.lat_avg_ms * o.requests + EXCLUDED.lat_avg_ms * EXCLUDED.requests) / (o.requests + EXCLUDED.requests) END,
			lat_p95_max_ms = GREATEST(o.lat_p95_max_ms, EXCLUDED.lat_p95_max_ms),
			pool_in_use = GREATEST(o.pool_in_use, EXCLUDED.pool_in_use),
			pool_max = GREATEST(o.pool_max, EXCLUDED.pool_max),
			pool_waits = o.pool_waits + EXCLUDED.pool_waits,
			disk_free_min = LEAST(o.disk_free_min, EXCLUDED.disk_free_min),
			db_bytes = GREATEST(EXCLUDED.db_bytes, o.db_bytes),
			goroutines = GREATEST(o.goroutines, EXCLUDED.goroutines)`,
		h.Hour.UTC(), h.Samples, h.CPUAvg, h.CPUMax, unknownToNull(h.HostCPUAvg), unknownToNull(h.HostCPUMax), h.MemAvg, h.MemMax,
		int64(min(h.MemBytesMax, 1<<62)), unknownToNull(h.HostMemMax), h.Requests, h.Slow, h.Errors, h.LatAvgMs, h.LatP95Max,
		h.PoolInUseMax, h.PoolMax, h.PoolWaits, unknownToNull(h.DiskFreeMin), h.DBBytes, h.Goroutines)
	return err
}

// HealthHours devolve as horas desde from, da mais antiga para a mais nova.
func (p *Postgres) HealthHours(ctx context.Context, from time.Time) ([]srvhealth.Hour, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT hour, samples, cpu_avg, cpu_max, host_cpu_avg, host_cpu_max, mem_avg, mem_max, mem_bytes_max, host_mem_max,
		       requests, slow, errors, lat_avg_ms, lat_p95_max_ms, pool_in_use, pool_max, pool_waits, disk_free_min, db_bytes, goroutines
		FROM server_health_hourly WHERE hour >= $1 ORDER BY hour`, from.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []srvhealth.Hour
	for rows.Next() {
		var h srvhealth.Hour
		var hostAvg, hostMax, hostMem, disk *float64
		var cpuAvg, cpuMax, memAvg, memMax, latAvg, latMax float32
		var memBytes int64
		if err := rows.Scan(&h.Hour, &h.Samples, &cpuAvg, &cpuMax, &hostAvg, &hostMax, &memAvg, &memMax, &memBytes, &hostMem,
			&h.Requests, &h.Slow, &h.Errors, &latAvg, &latMax, &h.PoolInUseMax, &h.PoolMax, &h.PoolWaits, &disk, &h.DBBytes, &h.Goroutines); err != nil {
			return nil, err
		}
		h.CPUAvg, h.CPUMax, h.MemAvg, h.MemMax = float64(cpuAvg), float64(cpuMax), float64(memAvg), float64(memMax)
		h.LatAvgMs, h.LatP95Max = float64(latAvg), float64(latMax)
		h.MemBytesMax = uint64(max(0, memBytes)) // #nosec G115 -- não negativo
		h.HostCPUAvg, h.HostCPUMax, h.HostMemMax, h.DiskFreeMin = nullToUnknown(hostAvg), nullToUnknown(hostMax), nullToUnknown(hostMem), nullToUnknown(disk)
		out = append(out, h)
	}
	return out, rows.Err()
}

// PruneHealth apaga as horas anteriores a before.
func (p *Postgres) PruneHealth(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM server_health_hourly WHERE hour < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
