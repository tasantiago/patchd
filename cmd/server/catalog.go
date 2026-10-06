package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/catalog/osv"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

const catalogUsage = `uso: patchd-server catalog <comando> [opções]

comandos:
  sync [-source msrc,ubuntu] [-months N] [-max N] [-workers N]
                            baixa o que é novo ou foi revisado em cada fonte (padrão: todas)
                              -months: janela de meses do MSRC (padrão 12)
                              -max: teto de USNs baixadas nesta execução (padrão 0, sem teto)
                              -workers: downloads simultâneos do Ubuntu (padrão 4)
  list                      lista os documentos do MSRC e resume o catálogo do Ubuntu
  builds [-product PREFIXO] maior build corrigido por produto e documento (padrão "Windows")
  usn -package NOME [-release VERSÃO]
                            avisos do Ubuntu de um pacote fonte; -release 26.04 filtra
                            pela versão (sem o Ubuntu Pro)

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
PATCHD_MSRC_URL troca a API do MSRC por outro endereço (padrão: https://api.msrc.microsoft.com/cvrf/v3.0).
PATCHD_OSV_URL troca o bucket do OSV por outro endereço (padrão: https://storage.googleapis.com/osv-vulnerabilities).
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
	months := fs.Int("months", 12, "janela de meses do MSRC no sync")
	sources := fs.String("source", "msrc,ubuntu", "fontes do sync, separadas por vírgula")
	maxUSN := fs.Int("max", 0, "teto de USNs baixadas no sync (0: sem teto)")
	workers := fs.Int("workers", 4, "downloads simultâneos do Ubuntu no sync")
	product := fs.String("product", "Windows", "prefixo do nome do produto no builds")
	pkg := fs.String("package", "", "pacote fonte no usn")
	release := fs.String("release", "", "versão do Ubuntu no usn, ex.: 26.04")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, catalogUsage)
		return exitConfig
	}
	if *months < 1 || *months > 120 {
		fmt.Fprintln(stderr, "patchd-server: -months deve ficar entre 1 e 120")
		return exitConfig
	}
	if *maxUSN < 0 {
		fmt.Fprintln(stderr, "patchd-server: -max não pode ser negativo")
		return exitConfig
	}
	if *workers < 1 || *workers > 16 {
		fmt.Fprintln(stderr, "patchd-server: -workers deve ficar entre 1 e 16")
		return exitConfig
	}
	wanted := map[string]bool{}
	for _, s := range strings.Split(*sources, ",") {
		switch s = strings.TrimSpace(s); s {
		case "msrc", "ubuntu":
			wanted[s] = true
		default:
			fmt.Fprintf(stderr, "patchd-server: fonte desconhecida %q em -source (use msrc e/ou ubuntu)\n", s)
			return exitConfig
		}
	}
	if args[0] == "usn" && *pkg == "" {
		fmt.Fprintln(stderr, "patchd-server: o usn exige -package (o pacote fonte, ex.: openssl)")
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
		ua := "patchd-server/" + buildinfo.Get().Version
		failed := false
		if wanted["msrc"] {
			src := msrc.NewClient(ua)
			// Outro endereço (um espelho interno, por exemplo); o padrão é a API pública do MSRC.
			if u, ok := look("PATCHD_MSRC_URL"); ok && u != "" {
				src.BaseURL = u
			}
			fmt.Fprintln(stdout, "MSRC:")
			since := time.Now().UTC().AddDate(0, -*months, 0)
			synced, nfail, err := syncMSRC(ctx, src, st, since, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				failed = true
			} else {
				fmt.Fprintf(stdout, "%d documento(s) gravado(s), %d com falha\n", synced, nfail)
				failed = failed || nfail > 0
			}
		}
		if wanted["ubuntu"] {
			src := osv.NewClient("Ubuntu", ua)
			if u, ok := look("PATCHD_OSV_URL"); ok && u != "" {
				src.BaseURL = u
			}
			fmt.Fprintln(stdout, "Ubuntu (OSV):")
			res, err := syncUbuntu(ctx, src, st, *maxUSN, *workers, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				failed = true
			}
			if remaining := res.Pending - res.Saved - res.Withdrawn - res.Failed; remaining > 0 && err == nil {
				fmt.Fprintf(stdout, "  ubuntu: %d ficam para a próxima execução (-max)\n", remaining)
			}
			failed = failed || res.Failed > 0
		}
		// Cada fonte roda mesmo que a anterior falhe; o código de saída diz se algo falhou.
		if failed {
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
		u, err := st.UbuntuSummary(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		newest := "-"
		if !u.Newest.IsZero() {
			newest = u.Newest.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(stdout, "\nUbuntu: %d USNs (mais %d retiradas), %d correções (ecossistema × pacote fonte), %d CVEs distintas; USN mais nova publicada em %s (UTC)\n",
			u.USNs, u.Withdrawn, u.Fixes, u.CVEs, newest)
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
	case "usn":
		fx, err := st.UbuntuFixes(ctx, *pkg, *release)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if len(fx) == 0 {
			fmt.Fprintf(stdout, "nenhum aviso para o pacote fonte %q\n", *pkg)
			return exitOK
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "USN\tPUBLICADA (UTC)\tECOSSISTEMA\tCORRIGIDO EM\tCVEs\tPRIORIDADE\tRESUMO")
		for _, f := range fx {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", f.USN, f.Published.Format("2006-01-02"), f.Ecosystem,
				f.Version, f.CVEs, f.TopPriority, f.Summary)
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
