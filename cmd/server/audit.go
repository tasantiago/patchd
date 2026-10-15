package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

// "patchd-server audit" (Aula 15.13): a trilha de auditoria no terminal, do mais novo para o
// mais antigo. Sem o IP de origem nem o request_id: a saída costuma ir para um chamado; os
// dois ficam no banco e no painel (Aula 15.14).

const auditUsage = `uso: patchd-server audit [-n 50] [-user NOME] [-action AÇÃO|PREFIXO.] [-since 24h]

Mostra a trilha de auditoria: logins, ações pelo painel e exportações.
  -action patch.aprovar   uma ação
  -action patch.          todas as que começam com "patch."
  -since 168h             só os últimos 7 dias
`

type auditReader interface {
	AuditEvents(ctx context.Context, f store.AuditFilter) ([]store.AuditEvent, error)
}

func runAudit(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: a trilha de auditoria exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	return auditCommand(ctx, store.NewPostgres(pool), args, time.Now(), stdout, stderr)
}

func auditCommand(ctx context.Context, st auditReader, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = io.WriteString(stderr, auditUsage) }
	n := fs.Int("n", 50, "quantos eventos")
	user := fs.String("user", "", "só deste usuário")
	action := fs.String("action", "", "só desta ação (ou prefixo terminado em ponto)")
	since := fs.Duration("since", 0, "só dos últimos (ex.: 24h)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		_, _ = io.WriteString(stderr, auditUsage)
		return exitConfig
	}
	if *n < 1 || *n > store.MaxAuditPage || *since < 0 {
		fmt.Fprintf(stderr, "patchd-server: -n de 1 a %d; -since positivo\n", store.MaxAuditPage)
		return exitConfig
	}
	f := store.AuditFilter{Username: strings.TrimSpace(*user), Action: strings.TrimSpace(*action), Limit: *n}
	if *since > 0 {
		f.Since = now.Add(-*since)
	}
	list, err := st.AuditEvents(ctx, f)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "nenhum evento com esse filtro")
		return exitOK
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "QUANDO (UTC)\tUSUÁRIO\tORIGEM\tAÇÃO\tALVO\tDETALHE")
	for _, e := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.At.UTC().Format("2006-01-02 15:04:05"), e.Username, dashIf(e.Source),
			api.AuditLabel(e.Action), dashIf(e.Target), dashIf(e.Detail))
	}
	_ = w.Flush()
	return exitOK
}

func dashIf(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
