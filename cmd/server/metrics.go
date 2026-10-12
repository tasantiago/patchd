package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/metrics"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Métricas (Aula 10.2): uma porta só para o Prometheus, com token. Os contadores da API
// sobem onde as coisas acontecem (internal/api); os medidores daqui são lidos do banco na
// hora da coleta, no máximo uma vez a cada snapshotTTL.

const (
	// snapshotTTL: coletas mais frequentes que isso recebem os números da anterior (a
	// avaliação da frota inteira não roda a cada 15 s só porque o Prometheus pediu).
	snapshotTTL = time.Minute
	// snapshotTimeout: banco lento não segura a coleta; o Prometheus desiste em 10 s.
	snapshotTimeout = 8 * time.Second
	// minMetricsToken: o token é gerado (gerar-segredos.sh), não escolhido por alguém.
	minMetricsToken = 24
)

// metricsConfig lê a porta e o token das métricas. Porta vazia: métricas desligadas.
func metricsConfig(look config.Lookup, others ...string) (addr, token string, err error) {
	addr = config.String(look, "PATCHD_METRICS_LISTEN_ADDR", "")
	token, err = config.Secret(look, "PATCHD_METRICS_TOKEN")
	if err != nil || addr == "" {
		return "", "", err
	}
	if err := validateListenAddr(addr); err != nil {
		return "", "", fmt.Errorf("PATCHD_METRICS_LISTEN_ADDR: %w", err)
	}
	for _, o := range others {
		if o != "" && o == addr {
			return "", "", errors.New("PATCHD_METRICS_LISTEN_ADDR: use uma porta só para as métricas")
		}
	}
	switch {
	case token == "":
		return "", "", errors.New("PATCHD_METRICS_LISTEN_ADDR exige PATCHD_METRICS_TOKEN_FILE (o /metrics nunca fica aberto)")
	case len(token) < minMetricsToken:
		return "", "", fmt.Errorf("PATCHD_METRICS_TOKEN: use pelo menos %d caracteres aleatórios (o gerar-segredos.sh cria um)", minMetricsToken)
	}
	return addr, token, nil
}

// metricsStore é o que os medidores leem do banco.
type metricsStore interface {
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	JobStateCounts(ctx context.Context) (map[string]int, error)
	CatalogStatus(ctx context.Context, sources []string) ([]store.CatalogSourceStatus, error)
	LastBackup(ctx context.Context) (store.BackupRun, bool, error)
}

// fleetCounter conta as máquinas por estado de compliance (o complianceService).
type fleetCounter interface {
	Fleet(ctx context.Context) (protocol.ComplianceFleet, error)
}

// snapshot são os números do banco de uma coleta.
type snapshot struct {
	ok         bool
	took       time.Duration
	channels   map[string]int // canal do último check-in das máquinas ativas
	compliance map[string]int
	jobs       map[string]int
	catalog    []store.CatalogSourceStatus
	backup     *store.BackupRun // nil: nenhuma cópia registrada (Aula 10.3)
}

type snapshotter struct {
	st     metricsStore
	fleet  fleetCounter // nil: sem compliance
	logger *slog.Logger
	now    func() time.Time

	mu   sync.Mutex // uma leitura do banco por vez; as coletas simultâneas esperam por ela
	at   time.Time
	last snapshot
}

func newSnapshotter(st metricsStore, fleet fleetCounter, logger *slog.Logger) *snapshotter {
	return &snapshotter{st: st, fleet: fleet, logger: logger, now: time.Now}
}

// get devolve os números da última leitura, ou lê de novo se ela tem mais de snapshotTTL.
func (s *snapshotter) get() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.at.IsZero() && now.Sub(s.at) < snapshotTTL {
		return s.last
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	snap, err := s.read(ctx)
	snap.took = s.now().Sub(now)
	if err != nil {
		// Sem números velhos: um medidor congelado engana mais que um buraco no gráfico.
		s.logger.Warn("métricas: leitura do banco falhou", "error", err)
		snap = snapshot{took: snap.took}
	}
	s.at, s.last = now, snap
	return snap
}

func (s *snapshotter) read(ctx context.Context) (snapshot, error) {
	snap := snapshot{ok: true, channels: map[string]int{"https": 0, "http": 0, "nenhum": 0}}
	list, err := s.st.Machines(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("máquinas: %w", err)
	}
	for _, m := range list {
		switch {
		case m.CheckinTLS == nil:
			snap.channels["nenhum"]++
		case *m.CheckinTLS:
			snap.channels["https"]++
		default:
			snap.channels["http"]++
		}
	}
	if snap.jobs, err = s.st.JobStateCounts(ctx); err != nil {
		return snapshot{}, fmt.Errorf("jobs: %w", err)
	}
	for _, st := range []string{jobs.StatePending, jobs.StateDelivered, jobs.StateDone, jobs.StateFailed,
		jobs.StateRejected, jobs.StateCanceled, jobs.StateExpired} {
		snap.jobs[st] += 0 // os sete sempre presentes: "falhou" zerado também é um número
	}
	if snap.catalog, err = s.st.CatalogStatus(ctx, catalogSourceNames); err != nil {
		return snapshot{}, fmt.Errorf("catálogo: %w", err)
	}
	b, found, err := s.st.LastBackup(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("cópias: %w", err)
	}
	if found {
		snap.backup = &b
	}
	if s.fleet != nil {
		f, err := s.fleet.Fleet(ctx)
		if err != nil {
			return snapshot{}, fmt.Errorf("compliance: %w", err)
		}
		snap.compliance = f.Counts
	}
	return snap, nil
}

// certExpiry é o que o medidor do certificado usa do certReloader.
type certExpiry interface {
	NotAfter() time.Time
}

// registerGauges registra os medidores. snap e certs podem ser nil (sem banco, sem TLS).
func registerGauges(reg *metrics.Registry, snap *snapshotter, certs certExpiry, info buildinfo.Info, started time.Time) {
	one := func(v float64) []metrics.Sample { return []metrics.Sample{{Value: v}} }
	reg.NewGaugeFunc("patchd_build_info", "Versão e commit deste servidor (o valor é sempre 1).",
		[]string{"version", "commit"}, func() []metrics.Sample {
			return []metrics.Sample{{Labels: []string{info.Version, info.Commit}, Value: 1}}
		})
	reg.NewGaugeFunc("patchd_start_time_seconds", "Quando o processo subiu (Unix).", nil,
		func() []metrics.Sample { return one(float64(started.Unix())) })
	reg.NewGaugeFunc("go_goroutines", "Goroutines em execução.", nil,
		func() []metrics.Sample { return one(float64(runtime.NumGoroutine())) })
	if certs != nil {
		reg.NewGaugeFunc("patchd_tls_cert_not_after_timestamp_seconds", "Fim da validade do certificado TLS em uso (Unix).", nil,
			func() []metrics.Sample { return one(float64(certs.NotAfter().Unix())) })
	}
	if snap == nil {
		return
	}
	bySnap := func(pick func(snapshot) map[string]int) func() []metrics.Sample {
		return func() []metrics.Sample {
			s := snap.get()
			if !s.ok {
				return nil
			}
			return samples(pick(s))
		}
	}
	reg.NewGaugeFunc("patchd_coleta_banco_ok", "1 se a última leitura do banco para as métricas deu certo.", nil,
		func() []metrics.Sample {
			if snap.get().ok {
				return one(1)
			}
			return one(0)
		})
	reg.NewGaugeFunc("patchd_coleta_banco_segundos", "Quanto levou a última leitura do banco para as métricas.", nil,
		func() []metrics.Sample { return one(snap.get().took.Seconds()) })
	reg.NewGaugeFunc("patchd_machines", "Máquinas ativas, pelo canal do último check-in (https, http ou nenhum).",
		[]string{"canal"}, bySnap(func(s snapshot) map[string]int { return s.channels }))
	if snap.fleet != nil {
		reg.NewGaugeFunc("patchd_compliance_machines", "Máquinas ativas por estado de compliance (RF-23).",
			[]string{"estado"}, bySnap(func(s snapshot) map[string]int { return s.compliance }))
	}
	reg.NewGaugeFunc("patchd_jobs", "Jobs por estado (todos os já criados).",
		[]string{"estado"}, bySnap(func(s snapshot) map[string]int { return s.jobs }))
	reg.NewGaugeFunc("patchd_catalog_last_success_timestamp_seconds", "Última sincronização com sucesso de cada fonte do catálogo (Unix).",
		[]string{"fonte"}, func() []metrics.Sample {
			var out []metrics.Sample
			for _, c := range snap.get().catalog {
				if !c.LastOK.IsZero() {
					out = append(out, metrics.Sample{Labels: []string{c.Source}, Value: float64(c.LastOK.Unix())})
				}
			}
			return out
		})
	reg.NewGaugeFunc("patchd_catalog_last_run_ok", "1 se a execução mais recente da fonte deu certo, 0 se falhou.",
		[]string{"fonte"}, func() []metrics.Sample {
			var out []metrics.Sample
			for _, c := range snap.get().catalog {
				if c.Runs > 0 {
					v := 0.0
					if c.Latest {
						v = 1
					}
					out = append(out, metrics.Sample{Labels: []string{c.Source}, Value: v})
				}
			}
			return out
		})
	registerBackupGauges(reg, snap)
}

// Cópias do banco (Aula 10.3): sem nenhuma registrada, as três séries não existem, e o
// alerta PatchdNenhumaCopia usa absent().
func registerBackupGauges(reg *metrics.Registry, snap *snapshotter) {
	last := func(v func(store.BackupRun) float64) func() []metrics.Sample {
		return func() []metrics.Sample {
			if b := snap.get().backup; b != nil {
				return []metrics.Sample{{Value: v(*b)}}
			}
			return nil
		}
	}
	reg.NewGaugeFunc("patchd_backup_last_success_timestamp_seconds", "Fim da cópia do banco mais recente registrada pelo backup.sh (Unix).", nil,
		last(func(b store.BackupRun) float64 { return float64(b.FinishedAt.Unix()) }))
	reg.NewGaugeFunc("patchd_backup_last_size_bytes", "Tamanho da cópia do banco mais recente.", nil,
		last(func(b store.BackupRun) float64 { return float64(b.Bytes) }))
	reg.NewGaugeFunc("patchd_backup_last_encrypted", "1 se a cópia do banco mais recente foi cifrada (age), 0 se não.", nil,
		last(func(b store.BackupRun) float64 {
			if b.Encrypted {
				return 1
			}
			return 0
		}))
}

func samples(m map[string]int) []metrics.Sample {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]metrics.Sample, 0, len(keys))
	for _, k := range keys {
		out = append(out, metrics.Sample{Labels: []string{k}, Value: float64(m[k])})
	}
	return out
}
