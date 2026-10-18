package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/metrics"
	"github.com/tasantiago/patchd/internal/store"
)

// Retenção (Aulas 6 e 10.5): o banco não cresce para sempre. Inventários e buscas além do
// atual de cada máquina saem depois de PATCHD_RETENTION_HISTORY_DAYS; jobs encerrados (e
// campanhas encerradas que ficaram sem jobs), depois de PATCHD_RETENTION_JOBS_DAYS.

const (
	// 90 dias de histórico cobrem um trimestre de auditoria com folga; com 1500 máquinas
	// e uma busca por dia, são cerca de 135 mil buscas.
	defaultHistoryDays = 90
	// 400 dias de jobs: um ano inteiro de "quem mandou reiniciar o quê", e mais um mês.
	defaultJobsDays = 400
	// Aula 18.1: 5 anos de trilha de auditoria. A tabela cresce devagar (logins e ações, não
	// máquinas); o prazo é decisão da instituição e muda por PATCHD_RETENTION_AUDIT_DAYS.
	defaultAuditDays = 1825
	pruneEvery       = time.Hour
)

// retentionConfig lê os dois cortes. Histórico: 30 a 3650 dias. Jobs: 0 (guardar sempre)
// ou 90 a 3650 dias.
func retentionConfig(look config.Lookup) (store.Retention, error) {
	days := func(key string, def, min int, zeroOK bool) (int, error) {
		v := config.String(look, key, strconv.Itoa(def))
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			return 0, fmt.Errorf("%s: use um número de dias (recebido %q)", key, v)
		case n == 0 && zeroOK:
			return 0, nil
		case n < min || n > 3650:
			if zeroOK {
				return 0, fmt.Errorf("%s: use 0 (guardar sempre) ou de %d a 3650 dias (recebido %d)", key, min, n)
			}
			return 0, fmt.Errorf("%s: use de %d a 3650 dias (recebido %d)", key, min, n)
		}
		return n, nil
	}
	h, err := days("PATCHD_RETENTION_HISTORY_DAYS", defaultHistoryDays, 30, false)
	if err != nil {
		return store.Retention{}, err
	}
	j, err := days("PATCHD_RETENTION_JOBS_DAYS", defaultJobsDays, 90, true)
	if err != nil {
		return store.Retention{}, err
	}
	a, err := days("PATCHD_RETENTION_AUDIT_DAYS", defaultAuditDays, 365, true)
	if err != nil {
		return store.Retention{}, err
	}
	return store.Retention{History: time.Duration(h) * 24 * time.Hour, Jobs: time.Duration(j) * 24 * time.Hour,
		Audit: time.Duration(a) * 24 * time.Hour}, nil
}

// describeRetention: "histórico 90 dias; jobs 400 dias" (ou "jobs guardados sempre").
func describeRetention(r store.Retention) string {
	jobs := "jobs guardados sempre"
	if r.Jobs > 0 {
		jobs = fmt.Sprintf("jobs e campanhas encerrados: %d dias", int(r.Jobs.Hours()/24))
	}
	audit := "auditoria guardada sempre"
	if r.Audit > 0 {
		audit = fmt.Sprintf("auditoria: %d dias", int(r.Audit.Hours()/24))
	}
	return fmt.Sprintf("inventários e buscas: %d dias (o atual de cada máquina fica sempre); %s; %s", int(r.History.Hours()/24), jobs, audit)
}

// pruner é o armazenamento que aplica a retenção (o PostgreSQL; a memória guarda só o
// último de cada coisa e não precisa).
type pruner interface {
	Prune(ctx context.Context, r store.Retention, now time.Time, dry bool) (store.PruneResult, error)
}

// retentionLoop aplica a retenção na partida e depois de hora em hora, até o encerramento.
// deleted (pode ser nil) conta as linhas apagadas por tabela, para as métricas.
func retentionLoop(ctx context.Context, logger *slog.Logger, p pruner, r store.Retention, deleted *metrics.CounterVec) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		res, err := p.Prune(ctx, r, time.Now(), false)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("retenção falhou", "error", err)
		case res.Total() > 0:
			logger.Info("retenção: linhas antigas apagadas",
				"inventarios", res.Inventories, "buscas", res.Scans, "jobs", res.Jobs, "campanhas", res.Campaigns, "auditoria", res.Audit)
			deleted.Add(float64(res.Audit), "auditoria")
			deleted.Add(float64(res.Inventories), "inventarios")
			deleted.Add(float64(res.Scans), "buscas")
			deleted.Add(float64(res.Jobs), "jobs")
			deleted.Add(float64(res.Campaigns), "campanhas")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

const retentionUsage = `uso: patchd-server retention [-apply]

Mostra o que a retenção apagaria agora e o tamanho das maiores tabelas. Nada é apagado
sem -apply. O servidor aplica a mesma retenção sozinho, de hora em hora; este comando é
para ver antes (ou aplicar já, depois de mudar os cortes).

  PATCHD_RETENTION_HISTORY_DAYS  inventários e buscas além do atual (padrão 90; 30 a 3650)
  PATCHD_RETENTION_JOBS_DAYS     jobs e campanhas encerrados (padrão 400; 0 = guardar sempre)
  PATCHD_RETENTION_AUDIT_DAYS    trilha de auditoria (padrão 1825; 0 = guardar sempre; 365 a 3650)
`

// retentionStore é o que o comando usa do banco.
type retentionStore interface {
	pruner
	TableSizes(ctx context.Context) (map[string]int64, int64, error)
}

func runRetention(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: a retenção exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	// Um banco grande, na primeira vez, pode levar alguns minutos.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	return retentionCommand(ctx, store.NewPostgres(pool), cfg.Retention, args, time.Now(), stdout, stderr)
}

func retentionCommand(ctx context.Context, st retentionStore, r store.Retention, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("retention", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = io.WriteString(stderr, retentionUsage) }
	apply := fs.Bool("apply", false, "apaga de verdade")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitConfig
	}
	before, total, err := st.TableSizes(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	res, err := st.Prune(ctx, r, now, !*apply)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "retenção: %s\n", describeRetention(r))
	if *apply {
		fmt.Fprintln(stdout, "linhas apagadas:")
	} else {
		fmt.Fprintln(stdout, "linhas que seriam apagadas (simulação: nada foi apagado; -apply apaga):")
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, l := range []struct {
		name string
		n    int64
	}{{"inventarios", res.Inventories}, {"buscas", res.Scans}, {"jobs", res.Jobs}, {"campanhas", res.Campaigns}, {"auditoria", res.Audit}} {
		fmt.Fprintf(w, "  %s\t%d\n", l.name, l.n)
	}
	_ = w.Flush()
	fmt.Fprintf(stdout, "banco: %s; maiores tabelas:\n", humanBytes(total))
	type tab struct {
		name string
		n    int64
	}
	var list []tab
	for k, v := range before {
		list = append(list, tab{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].n > list[j].n || (list[i].n == list[j].n && list[i].name < list[j].name)
	})
	if len(list) > 6 {
		list = list[:6]
	}
	for _, t := range list {
		fmt.Fprintf(w, "  %s\t%s\n", t.name, humanBytes(t.n))
	}
	_ = w.Flush()
	if *apply && res.Total() > 0 {
		fmt.Fprintln(stdout, "o espaço das linhas apagadas volta a ser usado pelo PostgreSQL (autovacuum); os arquivos não encolhem, e o tamanho para de crescer")
	}
	return exitOK
}

// humanBytes: 1536 -> "1.5 KiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
