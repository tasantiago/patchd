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

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

const catalogUsage = `uso: patchd-server catalog <comando> [opções]

comandos:
  sync [-source msrc,ubuntu,fedora,apple,kev,terceiros] [-months N] [-max N] [-workers N] [-full]
                            baixa o que é novo ou foi revisado em cada fonte (padrão: todas)
                              -months: janela de meses do MSRC (padrão 12)
                              -max: teto de USNs baixadas nesta execução (padrão 0, sem teto)
                              -workers: downloads simultâneos do Ubuntu (padrão 4)
                              -full: baixa de novo tudo, ignorando o que está guardado: no
                                     Fedora, todos os updates (pega bugs ligados depois do
                                     stable); no MSRC, todos os documentos da janela. Use com
                                     -source, para não baixar as duas fontes inteiras juntas
  status                    situação de cada fonte: último sucesso, última falha e se está atrasada
                            (sem sucesso há mais de 48 h); o servidor sincroniza sozinho a cada
                            PATCHD_CATALOG_INTERVAL (padrão 6h)
  list                      lista os documentos do MSRC e resume o Ubuntu e o Fedora
  kev [-days N]             CVEs exploradas (CISA KEV): cobertura de cada fonte e as incluídas
                            nos últimos N dias (padrão 30), com onde aparecem no catálogo
  apple [-release N]        majors do macOS: a versão de segurança mais recente (SOFA) ao lado
                            da publicada pela Apple (gdmf); -release 26 lista as versões da major
  terceiros                 regras do manifesto de programas de terceiros e a versão de referência
                            guardada de cada uma
  builds [-product PREFIXO] maior build corrigido por produto e documento (padrão "Windows")
  usn -package NOME [-release VERSÃO]
                            avisos do Ubuntu de um pacote fonte; -release 26.04 filtra
                            pela versão (sem o Ubuntu Pro)
  fedora -package NOME [-release F44]
                            updates de segurança do Fedora de um pacote fonte

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
PATCHD_MSRC_URL troca a API do MSRC por outro endereço (padrão: https://api.msrc.microsoft.com/cvrf/v3.0).
PATCHD_OSV_URL troca o bucket do OSV por outro endereço (padrão: https://storage.googleapis.com/osv-vulnerabilities).
PATCHD_BODHI_URL troca o Bodhi por outro endereço (padrão: https://bodhi.fedoraproject.org).
PATCHD_GDMF_URL e PATCHD_SOFA_URL trocam as fontes da Apple (padrão: https://gdmf.apple.com/v2/pmv
e https://sofafeed.macadmins.io/v2/macos_data_feed.json). O gdmf só é aceito com a cadeia da
raiz "Apple Root CA", embutida no binário.
PATCHD_KEV_URL troca o feed do KEV (padrão: o JSON da CISA; alternativa: o espelho oficial
https://raw.githubusercontent.com/cisagov/kev-data/develop/known_exploited_vulnerabilities.json).
PATCHD_THIRDPARTY_MANIFEST aponta um manifesto local de programas de terceiros, somado ao embutido
(regras com o mesmo id e SO substituem as do embutido). As fontes dos terceiros são as APIs do
Google (Chrome), da Mozilla (Firefox), do GitHub (winget-pkgs) e do Homebrew.
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
	sources := fs.String("source", "msrc,ubuntu,fedora,apple,kev,terceiros", "fontes do sync, separadas por vírgula")
	days := fs.Int("days", 30, "kev: janela de dias das CVEs incluídas recentemente")
	maxUSN := fs.Int("max", 0, "teto de USNs baixadas no sync (0: sem teto)")
	workers := fs.Int("workers", 4, "downloads simultâneos do Ubuntu no sync")
	product := fs.String("product", "Windows", "prefixo do nome do produto no builds")
	full := fs.Bool("full", false, "Fedora e MSRC: baixa tudo de novo no sync")
	pkg := fs.String("package", "", "pacote fonte no usn e no fedora")
	release := fs.String("release", "", "versão no usn (26.04), no fedora (F44) e no apple (26)")
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
		case "msrc", "ubuntu", "fedora", "apple", "kev", "terceiros":
			wanted[s] = true
		default:
			fmt.Fprintf(stderr, "patchd-server: fonte desconhecida %q em -source (use msrc, ubuntu, fedora, apple, kev e/ou terceiros)\n", s)
			return exitConfig
		}
	}
	if (args[0] == "usn" || args[0] == "fedora") && *pkg == "" {
		fmt.Fprintf(stderr, "patchd-server: o %s exige -package (o pacote fonte, ex.: openssl)\n", args[0])
		return exitConfig
	}
	if *days < 1 || *days > 3650 {
		fmt.Fprintln(stderr, "patchd-server: -days deve ficar entre 1 e 3650")
		return exitConfig
	}
	if args[0] == "apple" && *release != "" && !isDigits(*release) {
		fmt.Fprintf(stderr, "patchd-server: -release do apple é o número da major, ex.: 26 (recebido %q)\n", *release)
		return exitConfig
	}
	if args[0] == "fedora" && *release != "" && !bodhi.ValidRelease(*release) {
		fmt.Fprintf(stderr, "patchd-server: -release do fedora tem a forma F44 (recebido %q)\n", *release)
		return exitConfig
	}

	var manifest thirdparty.Manifest
	if args[0] == "terceiros" || (args[0] == "sync" && wanted["terceiros"]) {
		path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
		if manifest, err = thirdparty.Load(path); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
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
		opts := syncOptions{Sources: wanted, Months: *months, MaxUSN: *maxUSN, Workers: *workers, Full: *full, Trigger: "manual"}
		var results []sourceResult
		// A trava do banco impede que o agendamento do servidor e este comando sincronizem
		// ao mesmo tempo.
		err := withCatalogLock(ctx, st, func() {
			results = syncCatalog(ctx, newCatalogSources(look), st, opts, stdout)
		})
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		// Cada fonte roda mesmo que a anterior falhe; o código de saída diz se algo falhou.
		for _, r := range results {
			if !r.OK {
				return exitRuntime
			}
		}
		return exitOK
	case "status":
		ss, err := st.CatalogStatus(ctx, catalogSourceNames)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		printCatalogStatus(ss, time.Now(), stdout)
		return exitOK
	case "terceiros":
		refs, err := st.ThirdPartyVersions(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		printThirdPartyRules(manifest, refs, stdout)
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
		fsum, err := st.FedoraSummary(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		for _, f := range fsum {
			last := "-"
			if !f.LastPushed.IsZero() {
				last = f.LastPushed.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(stdout, "Fedora %s: %d updates de segurança (%d com CVEs incompletas), %d CVEs distintas; último publicado em %s (UTC)\n",
				f.Release, f.Updates, f.Partial, f.CVEs, last)
		}
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
	case "kev":
		info, err := st.KEVInfo(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		cov, err := st.KEVCoverage(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -*days)
		recent, err := st.KEVRecent(ctx, since)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		printKEV(info, cov, recent, *days, stdout)
		return exitOK
	case "apple":
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		if *release == "" {
			ms, err := st.AppleMajors(ctx)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			fmt.Fprintln(tw, "MAJOR\tSEGURANÇA MAIS RECENTE (SOFA)\tBUILDS\tDATA\tEXPLORADAS\tPUBLICADA (GDMF)\tCONFERE")
			for _, m := range ms {
				confere := "sim"
				switch {
				case m.GDMFLatest == "":
					confere = "fora do gdmf"
				case m.GDMFLatest != m.Latest:
					confere = "não"
				}
				gd := "-"
				if m.GDMFLatest != "" {
					gd = m.GDMFLatest + " " + strings.Join(m.GDMFBuilds, ",")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", m.Major, m.Latest, strings.Join(m.Builds, ","),
					m.Date.Format("2006-01-02"), m.Exploited, gd, confere)
			}
		} else {
			rs, err := st.AppleReleases(ctx, *release)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			if len(rs) == 0 {
				fmt.Fprintf(stdout, "nenhuma versão de segurança para a major %s\n", *release)
				return exitOK
			}
			fmt.Fprintln(tw, "VERSÃO\tBUILDS\tDATA\tCVEs\tEXPLORADAS\tBOLETIM")
			for _, r := range rs {
				b := strings.Join(r.Builds, ",")
				if b == "" {
					b = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", r.Version, b, r.Date.Format("2006-01-02"), r.CVEs, r.Exploited, r.URL)
			}
		}
		tw.Flush()
		return exitOK
	case "fedora":
		fx, err := st.FedoraFixes(ctx, *pkg, *release)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if len(fx) == 0 {
			fmt.Fprintf(stdout, "nenhum update de segurança para o pacote fonte %q\n", *pkg)
			return exitOK
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "UPDATE\tVERSÃO\tESTÁVEL (UTC)\tSEVERIDADE\tBUILD (ÉPOCA:NVR)\tCVEs")
		for _, f := range fx {
			stable := "-"
			if !f.Stable.IsZero() {
				stable = f.Stable.Format("2006-01-02")
			}
			cves := fmt.Sprint(f.CVEs)
			if f.CVEsPartial {
				cves += "+ (incompleta)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d:%s\t%s\n", f.Alias, f.Release, stable, f.Severity, f.Epoch, f.NVR, cves)
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
// revisão diferente da guardada. Um documento guardado que não está mais na lista é
// avisado e mantido: em 06/10/2026 o MSRC tirou da lista o 2026-Aug e o 2026-Sep por
// algumas horas, e o sync os ignorava em silêncio. A API do MSRC não aceita requisição condicional, então
// a lista (pequena) é o que evita baixar dezenas de MB à toa. Um documento que falha não
// impede os outros.
func syncMSRC(ctx context.Context, src msrcSource, st msrcStore, since time.Time, out io.Writer) (synced, failed int, err error) {
	r, err := syncMSRCWith(ctx, src, st, since, false, out)
	return r.Synced, r.Failed, err
}

// msrcResult resume uma sincronização do MSRC.
type msrcResult struct {
	Synced, Failed, Missing int
}

// syncMSRCWith é o syncMSRC com a ressincronização: full baixa de novo todos os documentos
// da janela, mesmo os com a data de revisão igual à guardada (um documento gravado errado,
// ou um parser novo que extrai mais campos).
func syncMSRCWith(ctx context.Context, src msrcSource, st msrcStore, since time.Time, full bool, out io.Writer) (res msrcResult, err error) {
	updates, err := src.Updates(ctx)
	if err != nil {
		return res, fmt.Errorf("lista do MSRC: %w", err)
	}
	known, err := st.MSRCVersions(ctx)
	if err != nil {
		return res, err
	}
	// Do mais velho para o mais novo, numa cópia: a lista recebida não é alterada.
	updates = slices.Clone(updates)
	slices.SortFunc(updates, func(a, b msrc.Update) int { return a.InitialReleaseDate.Compare(b.InitialReleaseDate) })

	for _, u := range updates {
		if u.InitialReleaseDate.Before(since) {
			continue
		}
		prev, seen := known[u.ID]
		if seen && prev.Equal(u.CurrentReleaseDate) && !full {
			fmt.Fprintf(out, "  %-9s em dia\n", u.ID)
			continue
		}
		start := time.Now()
		d, err := src.Document(ctx, u.ID)
		if err == nil {
			err = st.SaveMSRCDocument(ctx, d, u.CurrentReleaseDate)
		}
		if err != nil {
			res.Failed++
			fmt.Fprintf(out, "  %-9s FALHOU: %v\n", u.ID, err)
			continue
		}
		res.Synced++
		state := "novo"
		switch {
		case seen && prev.Equal(u.CurrentReleaseDate):
			state = "de novo"
		case seen:
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

	listed := map[string]bool{}
	for _, u := range updates {
		listed[u.ID] = true
	}
	var missing []string
	for id := range known {
		if !listed[id] {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	for _, id := range missing {
		fmt.Fprintf(out, "  %-9s AUSENTE da lista do MSRC: a versão guardada (revisão de %s UTC) foi mantida\n",
			id, known[id].Format("2006-01-02 15:04"))
	}
	res.Missing = len(missing)
	return res, nil
}

// isDigits diz se s é um número sem sinal (o número de uma major do macOS).
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
