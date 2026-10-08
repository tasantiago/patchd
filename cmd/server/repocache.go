package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/repocache"
)

const (
	// defaultRepoCacheMaxAge: um pacote sem uso há 30 dias já foi substituído na frota toda.
	defaultRepoCacheMaxAge = 30 * 24 * time.Hour
	repoPruneEvery         = 24 * time.Hour
)

// repoCacheLimits lê os limites da limpeza: PATCHD_REPO_CACHE_MAX_AGE (padrão 720h; 0
// desliga) e PATCHD_REPO_CACHE_MAX_SIZE (ex.: 50G; padrão 0, sem limite de tamanho).
func repoCacheLimits(look config.Lookup) (repocache.PruneOptions, error) {
	var opts repocache.PruneOptions
	var problems []error
	age, err := config.Duration(look, "PATCHD_REPO_CACHE_MAX_AGE", defaultRepoCacheMaxAge)
	switch {
	case err != nil:
		problems = append(problems, err)
	case age != 0 && age < 24*time.Hour:
		problems = append(problems, fmt.Errorf("PATCHD_REPO_CACHE_MAX_AGE: use 0 (desligado) ou 24h ou mais (recebido %s)", age))
	default:
		opts.MaxAge = age
	}
	size, err := repocache.ParseSize(config.String(look, "PATCHD_REPO_CACHE_MAX_SIZE", "0"))
	if err != nil {
		problems = append(problems, fmt.Errorf("PATCHD_REPO_CACHE_MAX_SIZE: %w", err))
	}
	opts.MaxSize = size
	return opts, errors.Join(problems...)
}

// pruneRepoCacheLoop limpa o cache uma vez por dia; a primeira, entre 10 e 20 minutos
// depois da partida (o servidor recém-iniciado atende os agentes antes).
func pruneRepoCacheLoop(ctx context.Context, logger *slog.Logger, c *repocache.Cache, opts repocache.PruneOptions) {
	if opts.MaxAge == 0 && opts.MaxSize == 0 {
		logger.Info("limpeza do cache de repositórios desligada (PATCHD_REPO_CACHE_MAX_AGE=0 e sem PATCHD_REPO_CACHE_MAX_SIZE)")
		return
	}
	wait := 10*time.Minute + time.Duration(rand.Int64N(int64(10*time.Minute)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		start := time.Now()
		res, err := c.Prune(opts)
		if err != nil {
			logger.Error("limpeza do cache de repositórios falhou", "error", err)
		} else {
			logger.Info("limpeza do cache de repositórios", "removed", res.Removed, "removed_mib", mib(res.RemovedBytes),
				"by_age", res.ByAge, "by_size", res.BySize, "tmp", res.Tmp, "duration", time.Since(start).Round(time.Millisecond).String())
		}
		wait = repoPruneEvery
	}
}

func mib(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }

const repoCacheUsage = `uso: patchd-server repo-cache <comando> [opções]

comandos:
  status                 tamanho e último uso de cada pasta do cache
  prune [-dry-run]       aplica PATCHD_REPO_CACHE_MAX_AGE e PATCHD_REPO_CACHE_MAX_SIZE agora
                         (-max-age e -max-size sobrepõem; -dry-run só mostra o que sairia)
`

// runRepoCache é o "patchd-server repo-cache ...". Pode rodar com o servidor no ar.
func runRepoCache(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "status" && args[0] != "prune") {
		fmt.Fprint(stderr, repoCacheUsage)
		return exitConfig
	}
	dir := config.String(look, "PATCHD_REPO_CACHE_DIR", "")
	if dir == "" {
		fmt.Fprintln(stderr, "patchd-server: defina PATCHD_REPO_CACHE_DIR")
		return exitConfig
	}
	c, err := repocache.New(dir, config.String(look, "PATCHD_REPO_CACHE_HOSTS", repocache.DefaultHosts), "patchd-server", slog.New(slog.DiscardHandler))
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}

	if args[0] == "status" {
		areas, err := c.Status()
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		printAreas(stdout, areas)
		return exitOK
	}

	opts, err := repoCacheLimits(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	fs := flag.NewFlagSet("repo-cache prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "só mostra o que sairia")
	fs.DurationVar(&opts.MaxAge, "max-age", opts.MaxAge, "sem uso há mais que isso sai, ex.: 720h (0 desliga)")
	maxSize := fs.String("max-size", fmt.Sprint(opts.MaxSize), "tamanho máximo, ex.: 50G (0 desliga)")
	if err := fs.Parse(args[1:]); err != nil {
		return exitConfig
	}
	if opts.MaxSize, err = repocache.ParseSize(*maxSize); err != nil {
		fmt.Fprintf(stderr, "patchd-server: -max-size: %v\n", err)
		return exitConfig
	}
	res, err := c.Prune(opts)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	printAreas(stdout, res.Before)
	verb := "removidos"
	if opts.DryRun {
		verb = "sairiam (simulação)"
	}
	fmt.Fprintf(stdout, "\nlimites: sem uso por mais de %s; tamanho máximo %s\n", orOff(opts.MaxAge > 0, opts.MaxAge.String()), orOff(opts.MaxSize > 0, mib(opts.MaxSize)+" MiB"))
	fmt.Fprintf(stdout, "%s: %d arquivo(s), %s MiB (%d por idade, %d por tamanho); sobras de download: %d\n",
		verb, res.Removed, mib(res.RemovedBytes), res.ByAge, res.BySize, res.Tmp)
	return exitOK
}

func orOff(on bool, v string) string {
	if !on {
		return "(desligado)"
	}
	return v
}

func printAreas(w io.Writer, areas map[string]repocache.Area) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PASTA\tARQUIVOS\tMIB\tÚLTIMO USO MAIS ANTIGO")
	for _, name := range []string{"sha256", "pool", "index"} {
		a := areas[name]
		oldest := "-"
		if !a.Oldest.IsZero() {
			oldest = a.Oldest.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", name, a.Files, mib(a.Bytes), oldest)
	}
	tw.Flush()
}
