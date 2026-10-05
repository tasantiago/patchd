package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

const catalogUsage = `uso: patchd-server catalog <comando> [opções]

comandos:
  sync [-months N]          baixa do MSRC os documentos novos ou revisados dos últimos N meses (padrão 12)
  list                      lista os documentos guardados
  builds [-product PREFIXO] maior build corrigido por produto e documento (padrão "Windows")

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
PATCHD_MSRC_URL troca a API do MSRC por outro endereço (padrão: https://api.msrc.microsoft.com/cvrf/v3.0).
`

// runCatalog administra o catálogo de segurança direto no banco.
func runCatalog(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, catalogUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: o catálogo exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}

	fs := flag.NewFlagSet("catalog "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	months := fs.Int("months", 12, "janela de meses do sync")
	product := fs.String("product", "Windows", "prefixo do nome do produto no builds")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, catalogUsage)
		return exitConfig
	}
	if *months < 1 || *months > 120 {
		fmt.Fprintln(stderr, "patchd-server: -months deve ficar entre 1 e 120")
		return exitConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
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
	st := store.NewPostgres(pool)

	switch args[0] {
	case "sync":
		src := msrc.NewClient("patchd-server/" + buildinfo.Get().Version)
		// Outro endereço (um espelho interno, por exemplo); o padrão é a API pública do MSRC.
		if u, ok := look("PATCHD_MSRC_URL"); ok && u != "" {
			src.BaseURL = u
		}
		since := time.Now().UTC().AddDate(0, -*months, 0)
		synced, failed, err := syncMSRC(ctx, src, st, since, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%d documento(s) gravado(s), %d com falha\n", synced, failed)
		if failed > 0 {
			return exitRuntime
		}
		return exitOK
	case "list":
		docs, err := st.MSRCDocuments(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DOCUMENTO\tREVISÃO (UTC)\tCVEs\tEXPLORADAS\tCORREÇÕES\tBAIXADO (UTC)\tTÍTULO")
		for _, d := range docs {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", d.ID, d.Current.Format("2006-01-02 15:04"), d.Vulns, d.Exploited,
				d.Fixes, d.FetchedAt.Format("2006-01-02 15:04"), d.Title)
		}
		tw.Flush()
		return exitOK
	case "builds":
		bs, err := st.MSRCBuilds(ctx, *product)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PRODUTO\tDOCUMENTO\tBUILD CORRIGIDO\tKB")
		for _, b := range bs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Product, b.Document, b.Build, b.KBs)
		}
		tw.Flush()
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], catalogUsage)
		return exitConfig
	}
}

// msrcSource e msrcStore são o que a sincronização usa; os testes passam versões falsas.
type msrcSource interface {
	Updates(ctx context.Context) ([]msrc.Update, error)
	Document(ctx context.Context, id string) (msrc.Document, error)
}

type msrcStore interface {
	MSRCVersions(ctx context.Context) (map[string]time.Time, error)
	SaveMSRCDocument(ctx context.Context, d msrc.Document, current time.Time) error
}

// syncMSRC baixa a lista e, da janela pedida, só os documentos novos ou com data de
// revisão diferente da guardada. A API do MSRC não aceita requisição condicional, então
// a lista (pequena) é o que evita baixar dezenas de MB à toa. Um documento que falha não
// impede os outros.
func syncMSRC(ctx context.Context, src msrcSource, st msrcStore, since time.Time, out io.Writer) (synced, failed int, err error) {
	updates, err := src.Updates(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("lista do MSRC: %w", err)
	}
	known, err := st.MSRCVersions(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Do mais velho para o mais novo, numa cópia: a lista recebida não é alterada.
	updates = slices.Clone(updates)
	slices.SortFunc(updates, func(a, b msrc.Update) int { return a.InitialReleaseDate.Compare(b.InitialReleaseDate) })

	for _, u := range updates {
		if u.InitialReleaseDate.Before(since) {
			continue
		}
		prev, seen := known[u.ID]
		if seen && prev.Equal(u.CurrentReleaseDate) {
			fmt.Fprintf(out, "  %-9s em dia\n", u.ID)
			continue
		}
		start := time.Now()
		d, err := src.Document(ctx, u.ID)
		if err == nil {
			err = st.SaveMSRCDocument(ctx, d, u.CurrentReleaseDate)
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "  %-9s FALHOU: %v\n", u.ID, err)
			continue
		}
		synced++
		state := "novo"
		if seen {
			state = "revisado"
		}
		exploited := 0
		for _, v := range d.Vulns {
			if v.Exploited {
				exploited++
			}
		}
		fmt.Fprintf(out, "  %-9s %-8s %d CVEs (%d exploradas), %d correções (%d sem KB ignoradas), %s\n",
			u.ID, state, len(d.Vulns), exploited, len(d.Fixes), d.SkippedFix, time.Since(start).Round(100*time.Millisecond))
	}
	return synced, failed, nil
}
