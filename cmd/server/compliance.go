package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/version"
)

const complianceUsage = `uso: patchd-server compliance -machine ID [-v] [-como VERSÃO] [-build BUILD]

Avalia o inventário atual de uma máquina contra o catálogo. ID pode ser o começo do ID da
máquina, desde que case com uma só.
  Ubuntu e Fedora: os pacotes fonte com correção pendente, o kernel em execução e as CVEs
  exploradas (KEV). -v lista os avisos pendentes de cada pacote.
  Windows: as CVEs do produto do MSRC acima do build da máquina, por CVE. -v lista todas
  as pendentes, as corrigidas com correção relançada e as sem atualização cumulativa.
    -como VERSÃO  avalia como outra versão do mesmo Windows (ex.: 25H2), para versões que o
                  MSRC ainda não lista. Só é aceito se o build for do produto, ou se o UBR
                  da máquina for o de alguma atualização cumulativa dele (evidência).
    -build BUILD  simulação: avalia como se a máquina estivesse nesse build (ex.: 26200.8875).

macOS entra na parte 4 da Aula 6.4.
A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
`

// complianceStore é o que a avaliação usa do banco; os testes passam uma versão falsa.
type complianceStore interface {
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
	LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error)
	UbuntuEcosystemsKnown(ctx context.Context) ([]string, error)
	UbuntuAdvisories(ctx context.Context, ecosystem string, sources []string) ([]compliance.Advisory, error)
	FedoraReleasesKnown(ctx context.Context) ([]string, error)
	FedoraAdvisories(ctx context.Context, release string, sources []string) ([]compliance.Advisory, error)
	KEVEntries(ctx context.Context) ([]kev.Vuln, error)
	WindowsProducts(ctx context.Context) ([]compliance.MSRCProduct, error)
	WindowsFixes(ctx context.Context, productIDs []string) ([]compliance.WindowsFix, error)
}

// evalOptions são as opções do operador que mudam a avaliação (só Windows, por enquanto).
type evalOptions struct {
	Assume string // -como: versão do Windows a usar no lugar da do inventário
	Build  string // -build: build simulado
}

// machineEval é a avaliação de uma máquina, pronta para imprimir.
type machineEval struct {
	ID        string
	OS        protocol.OSInfo
	Collected time.Time
	Catalog   string // ecossistema (Ubuntu), versão (Fedora) ou produto do MSRC (Windows)
	Linux     *compliance.LinuxResult
	Windows   *compliance.WindowsResult
	WinTarget compliance.WindowsTarget
	Simulated bool   // Windows: o build veio de -build
	Note      string // por que não houve avaliação (SO ainda não suportado, versão fora do catálogo)
}

var errNoMachine = errors.New("nenhuma máquina com esse ID")

func runCompliance(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compliance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	machine := fs.String("machine", "", "ID (ou começo do ID) da máquina")
	verbose := fs.Bool("v", false, "lista os avisos pendentes de cada pacote (Linux) ou todas as CVEs pendentes (Windows)")
	var opts evalOptions
	fs.StringVar(&opts.Assume, "como", "", "Windows: avalia como esta versão (ex.: 25H2)")
	fs.StringVar(&opts.Build, "build", "", "Windows: simula este build (ex.: 26200.8875)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *machine == "" {
		fmt.Fprint(stderr, complianceUsage)
		return exitConfig
	}
	if _, ok := compliance.ParseWindowsBuild(opts.Build); opts.Build != "" && !ok {
		fmt.Fprintf(stderr, "patchd-server: -build %q: use build.UBR, ex.: 26200.8875\n", opts.Build)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: o compliance exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	ev, err := evaluateMachine(ctx, store.NewPostgres(pool), *machine, opts)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	printEval(ev, *verbose, time.Now(), stdout)
	return exitOK
}

// evaluateMachine carrega o inventário atual e o avalia contra o catálogo da distribuição.
func evaluateMachine(ctx context.Context, st complianceStore, prefix string, opts evalOptions) (machineEval, error) {
	id, ok, err := st.ResolveMachine(ctx, prefix)
	if err != nil {
		return machineEval{}, err
	}
	if !ok {
		return machineEval{}, fmt.Errorf("%q: %w", prefix, errNoMachine)
	}
	inv, ok, err := st.LatestInventory(ctx, id)
	if err != nil {
		return machineEval{}, err
	}
	ev := machineEval{ID: id}
	if !ok {
		ev.Note = "a máquina ainda não enviou inventário"
		return ev, nil
	}
	ev.OS, ev.Collected = inv.OS, inv.CollectedAt
	if (opts.Assume != "" || opts.Build != "") && inv.OS.ID != "windows" {
		return ev, fmt.Errorf("-como e -build só valem para máquinas Windows (esta é %s)", inv.OS.ID)
	}

	sources := sourcePackages(inv.Software)
	switch inv.OS.ID {
	case "ubuntu":
		known, err := st.UbuntuEcosystemsKnown(ctx)
		if err != nil {
			return ev, err
		}
		eco, ok := compliance.UbuntuEcosystem(inv.OS.Version, known)
		if !ok {
			ev.Note = fmt.Sprintf("Ubuntu %s não está no catálogo do OSV: versão sem suporte, ou o catálogo do Ubuntu não foi sincronizado", inv.OS.Version)
			return ev, nil
		}
		adv, err := st.UbuntuAdvisories(ctx, eco, sources)
		if err != nil {
			return ev, err
		}
		r := compliance.EvaluateLinux(version.Dpkg, inv.Software, inv.OS.Kernel, adv)
		ev.Catalog, ev.Linux = eco, &r
	case "fedora":
		known, err := st.FedoraReleasesKnown(ctx)
		if err != nil {
			return ev, err
		}
		rel, ok := compliance.FedoraRelease(inv.OS.Version, known)
		if !ok {
			ev.Note = fmt.Sprintf("Fedora %s não está no catálogo do Bodhi: versão fora de suporte (archived), ou o catálogo do Fedora não foi sincronizado", inv.OS.Version)
			return ev, nil
		}
		adv, err := st.FedoraAdvisories(ctx, rel, sources)
		if err != nil {
			return ev, err
		}
		vulns, err := st.KEVEntries(ctx)
		if err != nil {
			return ev, err
		}
		compliance.InferFedoraKEV(adv, vulns)
		r := compliance.EvaluateLinux(version.RPM, inv.Software, inv.OS.Kernel, adv)
		ev.Catalog, ev.Linux = rel, &r
	case "windows":
		if err := evaluateWindows(ctx, st, inv.OS, opts, &ev); err != nil {
			return ev, err
		}
	default:
		ev.Note = fmt.Sprintf("a avaliação de %s %s entra nas próximas partes da Aula 6.4", inv.OS.Name, inv.OS.Version)
	}
	return ev, nil
}

// evaluateWindows escolhe o produto do MSRC e compara o build da máquina, CVE por CVE.
func evaluateWindows(ctx context.Context, st complianceStore, info protocol.OSInfo, opts evalOptions, ev *machineEval) error {
	if opts.Build != "" {
		info.Build, ev.Simulated = opts.Build, true
	}
	products, err := st.WindowsProducts(ctx)
	if err != nil {
		return err
	}
	t, note, ok := compliance.WindowsProductFor(info, opts.Assume, products)
	if !ok {
		ev.Note = note
		return nil
	}
	build, _ := compliance.ParseWindowsBuild(info.Build) // já validado por WindowsProductFor
	rows, err := st.WindowsFixes(ctx, t.IDs)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		ev.Note = fmt.Sprintf("o catálogo do MSRC não tem CVEs para %s (rode catalog sync -source msrc)", t.Name)
		return nil
	}
	r := compliance.EvaluateWindows(build, rows)
	if r.Foreign {
		majors := joinInts(r.Majors)
		switch {
		case !t.Assumed:
			ev.Note = fmt.Sprintf("o build %d da máquina não é de %s (builds do produto: %s); comparar só o UBR seria comparar linhas de atualização diferentes",
				build.Major, t.Name, majors)
			return nil
		case r.Evidence == "":
			ev.Note = fmt.Sprintf("-como %s recusado: o build %d não é do produto (builds: %s) e o UBR %d não aparece em nenhuma atualização cumulativa dele, então não há evidência de que a máquina siga essa linha de atualizações",
				t.Version, build.Major, majors, build.UBR)
			return nil
		}
	}
	ev.Catalog, ev.Windows, ev.WinTarget = t.Name, &r, t
	return nil
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

func sourcePackages(sw []protocol.Software) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sw {
		if s.SourcePackage != "" && !seen[s.SourcePackage] {
			seen[s.SourcePackage] = true
			out = append(out, s.SourcePackage)
		}
	}
	return out
}

// printEval escreve a avaliação. now é parâmetro para os testes.
func printEval(ev machineEval, verbose bool, now time.Time, out io.Writer) {
	osName := strings.TrimSpace(ev.OS.Name + " " + ev.OS.Version)
	fmt.Fprintf(out, "Máquina %s", ev.ID)
	if osName != "" {
		fmt.Fprintf(out, " (%s)", osName)
	}
	fmt.Fprintln(out)
	if !ev.Collected.IsZero() {
		age := now.Sub(ev.Collected)
		fmt.Fprintf(out, "Inventário coletado em %s UTC", ev.Collected.UTC().Format("2006-01-02 15:04"))
		if age > 48*time.Hour {
			fmt.Fprintf(out, " (ATENÇÃO: há %d dias; a avaliação vale para aquele momento)", int(age.Hours()/24))
		}
		fmt.Fprintln(out)
	}
	if ev.Note != "" {
		fmt.Fprintf(out, "Sem avaliação: %s\n", ev.Note)
		return
	}
	if ev.Windows != nil {
		printWindows(ev, verbose, out)
		return
	}
	r := ev.Linux
	fmt.Fprintf(out, "Catálogo: %s\n", ev.Catalog)

	k := r.Kernel
	switch {
	case k.Release == "":
		fmt.Fprintln(out, "Kernel: o inventário não informa o kernel em execução; todas as versões instaladas foram avaliadas")
	case k.Source == "":
		fmt.Fprintf(out, "Kernel em execução: %s (não identificado no inventário; todas as versões instaladas foram avaliadas)\n", k.Release)
	default:
		fmt.Fprintf(out, "Kernel em execução: %s (fonte %s %s)\n", k.Release, k.Source, k.Version)
		if len(k.Newer) > 0 {
			fmt.Fprintf(out, "  REBOOT PENDENTE: kernel mais novo instalado e não carregado: %s\n", strings.Join(k.Newer, ", "))
		}
		if len(k.Older) > 0 {
			fmt.Fprintf(out, "  versões antigas ainda instaladas (fora da conformidade): %s\n", strings.Join(k.Older, ", "))
		}
	}

	exploited, inferred := 0, 0
	for _, f := range r.Findings {
		switch {
		case f.Exploited():
			exploited++
		case f.InferredExploited():
			inferred++
		}
	}
	fmt.Fprintf(out, "%d pacotes fonte avaliados; %d com correção pendente (%d com CVE explorada no KEV, %d com exploração inferida)\n",
		r.Sources, len(r.Findings), exploited, inferred)
	if len(r.Invalid) > 0 {
		fmt.Fprintf(out, "%d comparação(ões) com versão inválida, fora da conta (ex.: %s)\n", len(r.Invalid), r.Invalid[0])
	}
	if len(r.Findings) == 0 {
		return
	}

	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EXPLORADA\tFONTE\tINSTALADA\tCORRIGIDA EM\tAVISOS\tBINÁRIOS")
	for _, f := range r.Findings {
		mark := ""
		switch {
		case f.Exploited():
			mark = "KEV"
		case f.InferredExploited():
			mark = "inferida"
		}
		bins := f.Binaries
		more := ""
		if len(bins) > 3 {
			more = fmt.Sprintf(" +%d", len(bins)-3)
			bins = bins[:3]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d (%s)\t%s%s\n", mark, f.Source, f.Installed, f.Target,
			len(f.Advisories), f.Advisories[0].ID, strings.Join(bins, ","), more)
		if verbose {
			for _, a := range f.Advisories {
				extra := ""
				if len(a.KEV) > 0 {
					extra += " KEV: " + strings.Join(a.KEV, ",")
				}
				if len(a.Inferred) > 0 {
					extra += " KEV inferido: " + strings.Join(a.Inferred, ",")
				}
				partial := ""
				if a.Partial {
					partial = "+"
				}
				fmt.Fprintf(tw, "\t  %s\t%s\t%s\t%s, %d%s CVEs%s\t\n", a.ID, a.Published.Format("2006-01-02"), a.Fixed,
					a.Severity, len(a.CVEs), partial, extra)
			}
		}
	}
	tw.Flush()
}

// maxWindowsRows é quantas CVEs pendentes aparecem sem -v.
const maxWindowsRows = 20

// printWindows escreve a avaliação de uma máquina Windows.
func printWindows(ev machineEval, verbose bool, out io.Writer) {
	r, t := ev.Windows, ev.WinTarget
	fmt.Fprintf(out, "Catálogo: %s\n", ev.Catalog)
	if t.Assumed && !strings.EqualFold(t.Version, ev.OS.Version) {
		fmt.Fprintf(out, "  PRESUMIDO (-como %s): o inventário diz %s.", t.Version, ev.OS.Version)
		if r.Foreign {
			fmt.Fprintf(out, " O build %d não é do produto (builds: %s); evidência: o UBR %d é o do %s neste produto.",
				r.Build.Major, joinInts(r.Majors), r.Build.UBR, r.Evidence)
		}
		fmt.Fprintln(out)
	}
	if ev.Simulated {
		fmt.Fprintf(out, "SIMULAÇÃO (-build): avaliado no build %s; o inventário diz %s\n", r.Build, ev.OS.Build)
	} else {
		fmt.Fprintf(out, "Build: %s (UBR %d)\n", r.Build, r.Build.UBR)
	}

	explored := 0
	for _, c := range r.Pending {
		if c.Explored() {
			explored++
		}
	}
	fmt.Fprintf(out, "%d CVEs do produto no catálogo; %d pendentes (%d exploradas, pelo MSRC ou pelo KEV)\n",
		r.CVEs, len(r.Pending), explored)
	if len(r.Pending) > 0 {
		fmt.Fprintf(out, "Alvo: UBR %d (KB%s, build %s), a atualização cumulativa que corrige todas as pendentes\n",
			r.Target.UBR, r.TargetKB, r.Target)
	}
	if n := len(r.Rereleased); n > 0 {
		fmt.Fprintf(out, "%d CVE(s) corrigida(s), mas com correção relançada acima deste build (informação)\n", n)
	}
	if n := len(r.NoFix); n > 0 {
		fmt.Fprintf(out, "%d CVE(s) sem atualização cumulativa no catálogo para o produto (só hotpatch, ou sem KB): fora da conta\n", n)
	}
	var ignored []string
	if r.Hotpatch > 0 {
		ignored = append(ignored, fmt.Sprintf("%d de hotpatch (o inventário não diz se a máquina está inscrita)", r.Hotpatch))
	}
	kinds := make([]string, 0, len(r.Ignored))
	for k := range r.Ignored {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		ignored = append(ignored, fmt.Sprintf("%d %q", r.Ignored[k], k))
	}
	if len(ignored) > 0 {
		fmt.Fprintf(out, "Correções desconsideradas: %s\n", strings.Join(ignored, "; "))
	}

	fixed := func(c compliance.WindowsCVE) string {
		s := fmt.Sprintf(".%d (KB%s)", c.Min.UBR, c.MinKB)
		if verbose && c.Max.UBR != c.Min.UBR {
			s += fmt.Sprintf(", relançada em .%d (KB%s)", c.Max.UBR, c.MaxKB)
		}
		return s
	}
	mark := func(c compliance.WindowsCVE) string {
		switch {
		case c.KEV:
			return "KEV"
		case c.Exploited:
			return "MSRC"
		}
		return ""
	}
	table := func(title string, list []compliance.WindowsCVE, limit int) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(out, "\n%s\n", title)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "EXPLORADA\tCVE\tSEVERIDADE\tCVSS\tDOCUMENTO\tCORRIGIDA EM\tTÍTULO")
		for i, c := range list {
			if limit > 0 && i == limit {
				fmt.Fprintf(tw, "\t+%d (use -v)\t\t\t\t\t\n", len(list)-limit)
				break
			}
			corr := "-"
			if c.MinKB != "" {
				corr = fixed(c)
			}
			score := "-"
			if c.Score > 0 {
				score = strconv.FormatFloat(c.Score, 'f', 1, 64)
			}
			title := c.Title
			if rs := []rune(title); len(rs) > 60 {
				title = string(rs[:57]) + "..."
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark(c), c.CVE, c.Severity, score, c.Document, corr, title)
		}
		tw.Flush()
	}
	limit := maxWindowsRows
	if verbose {
		limit = 0
	}
	table("Pendentes:", r.Pending, limit)
	if verbose {
		table("Corrigidas, com correção relançada acima deste build:", r.Rereleased, 0)
		table("Sem atualização cumulativa no catálogo:", r.NoFix, 0)
	}
}
