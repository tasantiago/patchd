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
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

const complianceUsage = `uso: patchd-server compliance -machine ID [-v] [-como VERSÃO] [-build BUILD]
     patchd-server compliance -macos VERSÃO [-modelo ID] [-build BUILD] [-v]

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
  macOS: as CVEs corrigidas na major da máquina acima da versão dela, o suporte da major
  (pelo catálogo: recebeu versão de segurança no ciclo da major mais nova?) e o que o modelo
  do Mac permite (atualizar a major ou trocar o hardware). -v lista todas as pendentes.
    -macos VERSÃO simulação sem máquina (ex.: 14.8.9), com -modelo (ex.: Mac14,14) e -build
                  opcionais.
  Programas de terceiros (Windows e macOS): cada programa reconhecido pelo manifesto, com a
  situação (desatualizado, abaixo do mínimo, fora de suporte, coberto pelo catálogo do SO,
  em dia). As versões de referência vêm do catalog sync -source terceiros. -v lista também
  os programas sem regra. PATCHD_THIRDPARTY_MANIFEST acrescenta um manifesto local.
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
	MacOSMajors(ctx context.Context) ([]compliance.MacOSMajor, error)
	MacOSFixes(ctx context.Context, major int) ([]compliance.MacOSFix, error)
	MacOSUnfixed(ctx context.Context, major int, since time.Time) ([]compliance.MacOSCVE, error)
	AppleModel(ctx context.Context, model string) (compliance.MacModel, bool, error)
	AppleModelsCount(ctx context.Context) (int, error)
	AppleExtraBuild(ctx context.Context, build string) (compliance.MacExtra, bool, error)
	ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error)
}

// evalOptions são as opções do operador que mudam a avaliação.
type evalOptions struct {
	Assume string    // -como: versão do Windows a usar no lugar da do inventário
	Build  string    // -build: build simulado (Windows, ou o da simulação do macOS)
	MacOS  string    // -macos: simulação de um Mac, sem máquina
	Model  string    // -modelo: o modelo do Mac simulado
	Now    time.Time // relógio da avaliação (zero: agora); os testes fixam
	// Manifesto dos programas de terceiros; nil: não avalia os terceiros.
	Manifest *thirdparty.Manifest
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
	MacOS     *macEval
	Simulated bool // Windows: o build veio de -build
	NoMachine bool // simulação sem máquina (-macos)
	Third     *thirdparty.Result
	Note      string // por que não houve avaliação (SO ainda não suportado, versão fora do catálogo)
}

// macEval é a avaliação de um Mac.
type macEval struct {
	Result  compliance.MacOSResult
	Support compliance.MacOSSupport
	ModelID string
	Model   *compliance.MacModel
	HWLabel string // ATUALIZAR A MAJOR, HARDWARE FORA DE SUPORTE, AVISO, ou vazio
	HWText  string
	Unfixed []compliance.MacOSCVE // major encerrada: exploradas corrigidas depois, só em outras majors
	Extra   *compliance.MacExtra
}

var errNoMachine = errors.New("nenhuma máquina com esse ID")

func runCompliance(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compliance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	machine := fs.String("machine", "", "ID (ou começo do ID) da máquina")
	verbose := fs.Bool("v", false, "lista os avisos pendentes de cada pacote (Linux) ou todas as CVEs pendentes (Windows)")
	var opts evalOptions
	fs.StringVar(&opts.Assume, "como", "", "Windows: avalia como esta versão (ex.: 25H2)")
	fs.StringVar(&opts.Build, "build", "", "simula este build (Windows: 26200.8875; macOS: 25G83)")
	fs.StringVar(&opts.MacOS, "macos", "", "simula um Mac nesta versão, sem máquina (ex.: 14.8.9)")
	fs.StringVar(&opts.Model, "modelo", "", "com -macos: o modelo do Mac (ex.: Mac14,14)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*machine == "") == (opts.MacOS == "") {
		fmt.Fprint(stderr, complianceUsage)
		return exitConfig
	}
	if opts.MacOS != "" && opts.Assume != "" {
		fmt.Fprintln(stderr, "patchd-server: -como só vale para Windows")
		return exitConfig
	}
	if opts.Model != "" && opts.MacOS == "" {
		fmt.Fprintln(stderr, "patchd-server: -modelo só vale com -macos (numa máquina real, o modelo vem do inventário)")
		return exitConfig
	}
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	manifest, err := thirdparty.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	opts.Manifest = &manifest
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
// Com -macos, não há máquina: o inventário é montado com a versão, o modelo e o build dados.
func evaluateMachine(ctx context.Context, st complianceStore, prefix string, opts evalOptions) (machineEval, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.MacOS != "" {
		ev := machineEval{NoMachine: true, OS: protocol.OSInfo{ID: "macos", Name: "macOS", Version: opts.MacOS, Build: opts.Build}}
		return ev, evaluateMacOS(ctx, st, ev.OS, opts.Model, opts.Now, &ev)
	}
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
	case "macos":
		model := ""
		if inv.Hardware != nil {
			model = inv.Hardware.Model
		}
		if err := evaluateMacOS(ctx, st, inv.OS, model, opts.Now, &ev); err != nil {
			return ev, err
		}
	default:
		ev.Note = fmt.Sprintf("%s %s: o patchd não tem catálogo para este sistema", inv.OS.Name, inv.OS.Version)
	}

	// Programas de terceiros (Windows e macOS), mesmo quando o SO ficou sem avaliação.
	if (inv.OS.ID == "windows" || inv.OS.ID == "macos") && opts.Manifest != nil && inv.Software != nil {
		refs, err := st.ThirdPartyVersions(ctx)
		if err != nil {
			return ev, err
		}
		r := thirdparty.Evaluate(inv.OS.ID, inv.Software, *opts.Manifest, refs)
		ev.Third = &r
	}
	return ev, nil
}

// evaluateWindows escolhe o produto do MSRC e compara o build da máquina, CVE por CVE.
func evaluateWindows(ctx context.Context, st complianceStore, info protocol.OSInfo, opts evalOptions, ev *machineEval) error {
	if opts.Build != "" {
		if _, ok := compliance.ParseWindowsBuild(opts.Build); !ok {
			return fmt.Errorf("-build %q: no Windows, use build.UBR (ex.: 26200.8875)", opts.Build)
		}
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

// evaluateMacOS avalia a versão do macOS dentro da major, o suporte da major e o modelo.
func evaluateMacOS(ctx context.Context, st complianceStore, info protocol.OSInfo, modelID string, now time.Time, ev *machineEval) error {
	major, ok := compliance.MacOSMajorOf(info.Version)
	if !ok {
		ev.Note = fmt.Sprintf("versão do macOS %q ilegível", info.Version)
		return nil
	}
	majors, err := st.MacOSMajors(ctx)
	if err != nil {
		return err
	}
	sup := compliance.MacOSSupportOf(majors, major, now)
	if sup.State == compliance.SupportUnknown {
		ev.Note = sup.Note
		return nil
	}
	m := &macEval{Support: sup, ModelID: modelID, Result: compliance.MacOSResult{Version: info.Version}}
	ev.Catalog = fmt.Sprintf("macOS %d", major)
	if sup.Major.Latest != "" { // a major está no catálogo
		ev.Catalog = "macOS " + sup.Major.Name
		fixes, err := st.MacOSFixes(ctx, major)
		if err != nil {
			return err
		}
		m.Result = compliance.EvaluateMacOS(info.Version, sup.Major.Latest, fixes)
		if sup.State == compliance.SupportEnded {
			if m.Unfixed, err = st.MacOSUnfixed(ctx, major, sup.Major.LatestDate); err != nil {
				return err
			}
			compliance.SortMacOSCVEs(m.Unfixed)
		}
	}
	if info.Build != "" {
		x, ok, err := st.AppleExtraBuild(ctx, info.Build)
		if err != nil {
			return err
		}
		if ok {
			m.Extra = &x
		}
	}
	switch {
	case modelID == "":
		m.HWText = "o inventário não informa o modelo do Mac"
	default:
		mod, ok, err := st.AppleModel(ctx, modelID)
		if err != nil {
			return err
		}
		if ok {
			m.Model = &mod
			m.HWLabel, m.HWText = compliance.MacOSHardware(mod, major, sup, majors, now)
			break
		}
		n, err := st.AppleModelsCount(ctx)
		if err != nil {
			return err
		}
		m.HWText = fmt.Sprintf("o modelo %s não está na lista do SOFA", modelID)
		if n == 0 {
			m.HWText = "o catálogo ainda não tem os modelos do SOFA (rode catalog sync -source apple)"
		}
	}
	ev.MacOS = m
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
	switch {
	case ev.NoMachine:
		fmt.Fprintf(out, "Simulação, sem máquina: %s", osName)
		if ev.MacOS != nil && ev.MacOS.ModelID != "" {
			fmt.Fprintf(out, ", modelo %s", ev.MacOS.ModelID)
		}
		if ev.OS.Build != "" {
			fmt.Fprintf(out, ", build %s", ev.OS.Build)
		}
		fmt.Fprintln(out)
	default:
		fmt.Fprintf(out, "Máquina %s", ev.ID)
		if osName != "" {
			fmt.Fprintf(out, " (%s)", osName)
		}
		fmt.Fprintln(out)
	}
	// Os terceiros saem no fim, depois do que o SO tiver (ou da nota de sem avaliação).
	defer printThirdParty(ev, verbose, now, out)
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
	if ev.MacOS != nil {
		printMacOS(ev, verbose, out)
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

// maxCVERows é quantas CVEs aparecem em cada tabela sem -v.
const maxCVERows = 20

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
	fmt.Fprintf(out, "%d CVEs do produto no catálogo; %s (%d exploradas, pelo MSRC ou pelo KEV)\n",
		r.CVEs, pendentes(len(r.Pending)), explored)
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
	limit := maxCVERows
	if verbose {
		limit = 0
	}
	table("Pendentes:", r.Pending, limit)
	if verbose {
		table("Corrigidas, com correção relançada acima deste build:", r.Rereleased, 0)
		table("Sem atualização cumulativa no catálogo:", r.NoFix, 0)
	}
}

// printMacOS escreve a avaliação de um Mac.
func printMacOS(ev machineEval, verbose bool, out io.Writer) {
	m, r, sup := ev.MacOS, ev.MacOS.Result, ev.MacOS.Support
	fmt.Fprintf(out, "Catálogo: %s (SOFA)", ev.Catalog)
	if sup.Major.Latest != "" {
		fmt.Fprintf(out, ": última versão de segurança %s, de %s", sup.Major.Latest, sup.Major.LatestDate.Format("2006-01-02"))
	}
	fmt.Fprintln(out)
	switch sup.State {
	case compliance.Supported:
		fmt.Fprintf(out, "Suporte: com suporte (%s)\n", sup.Note)
	case compliance.SupportUnconfirmed:
		fmt.Fprintf(out, "Suporte: A CONFIRMAR: %s\n", sup.Note)
	default:
		fmt.Fprintf(out, "Suporte: FIM DE SUPORTE: %s\n", sup.Note)
	}
	if r.Ahead {
		fmt.Fprintf(out, "  a versão %s é mais nova que a última do catálogo para a major (sincronize o SOFA)\n", r.Version)
	}
	if x := m.Extra; x != nil {
		fmt.Fprintf(out, "Build %s: melhoria de segurança em segundo plano %s %s, sobre o %s; o SOFA não traz as CVEs dela, e ela não conta como correção\n",
			x.Build, x.Version, x.Extra, x.Prerequisite)
	}
	switch {
	case m.Model != nil && m.HWLabel != "":
		fmt.Fprintf(out, "Modelo: %s (%s). %s: %s\n", m.Model.ID, m.Model.Name, m.HWLabel, m.HWText)
	case m.Model != nil:
		fmt.Fprintf(out, "Modelo: %s (%s): %s\n", m.Model.ID, m.Model.Name, m.HWText)
	default:
		fmt.Fprintf(out, "Modelo: %s\n", m.HWText)
	}

	exploited, kev := 0, 0
	for _, c := range r.Pending {
		if c.KEV {
			kev++
		}
		if c.Exploited || c.KEV {
			exploited++
		}
	}
	fmt.Fprintf(out, "%d CVEs corrigidas na major, no catálogo; %s (%d exploradas, %d no KEV)\n", r.CVEs, pendentes(len(r.Pending)), exploited, kev)
	if r.Target != "" {
		fmt.Fprintf(out, "Alvo: %s, a versão da major que corrige todas as pendentes\n", r.Target)
	}
	if len(r.Invalid) > 0 {
		fmt.Fprintf(out, "%d comparação(ões) com versão inválida, fora da conta (ex.: %s)\n", len(r.Invalid), r.Invalid[0])
	}
	if n := len(m.Unfixed); n > 0 {
		fmt.Fprintf(out, "%d CVE(s) explorada(s) corrigida(s) depois de %s só em outras majors: nesta não vai sair correção (a major pode nem ser afetada por todas; não há como saber)\n",
			n, sup.Major.LatestDate.Format("2006-01-02"))
	}

	limit := maxCVERows
	if verbose {
		limit = 0
	}
	table := func(title string, list []compliance.MacOSCVE, fixed func(compliance.MacOSCVE) string) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(out, "\n%s\n", title)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "EXPLORADA\tCVE\tSEVERIDADE\tCORRIGIDA EM\tDATA")
		for i, c := range list {
			if limit > 0 && i == limit {
				fmt.Fprintf(tw, "\t+%d (use -v)\t\t\t\n", len(list)-limit)
				break
			}
			mark := ""
			switch {
			case c.KEV:
				mark = "KEV"
			case c.Exploited:
				mark = "Apple"
			}
			sev := c.Severity
			if sev == "" {
				sev = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, c.CVE, sev, fixed(c), c.Released.Format("2006-01-02"))
		}
		tw.Flush()
	}
	table("Pendentes:", r.Pending, func(c compliance.MacOSCVE) string {
		if verbose && c.Max != c.Min {
			return c.Min + ", de novo em " + c.Max
		}
		return c.Min
	})
	table("Exploradas sem correção nesta major:", m.Unfixed, func(c compliance.MacOSCVE) string {
		return strings.Join(c.Elsewhere, ", ")
	})
}

// pendentes concorda o número: "1 pendente", "0 pendentes".
func pendentes(n int) string {
	if n == 1 {
		return "1 pendente"
	}
	return fmt.Sprintf("%d pendentes", n)
}

// printThirdParty escreve a avaliação dos programas de terceiros.
func printThirdParty(ev machineEval, verbose bool, now time.Time, out io.Writer) {
	r := ev.Third
	if r == nil {
		return
	}
	fmt.Fprintf(out, "\nProgramas de terceiros (%d regras para %s): %d no inventário, %d identificados, %d auxiliares, %d sem regra\n",
		r.Rules, ev.OS.ID, r.Programs, len(r.Findings), r.Auxiliary, len(r.Unmatched))
	var counts []string
	for _, c := range []struct{ state, label string }{
		{thirdparty.StateOutdated, "desatualizado"}, {thirdparty.StateBelowMinimum, "abaixo do mínimo"},
		{thirdparty.StateEOL, "fora de suporte"}, {thirdparty.StateBadVersion, "com versão ilegível"},
		{thirdparty.StateNoReference, "sem referência"}, {thirdparty.StateOSVendor, "no catálogo do SO"},
		{thirdparty.StateCurrent, "em dia"},
	} {
		if n := r.Count(c.state); n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, c.label))
		}
	}
	if len(counts) > 0 {
		fmt.Fprintf(out, "  %s\n", strings.Join(counts, ", "))
	}
	if r.Count(thirdparty.StateNoReference) > 0 {
		fmt.Fprintln(out, "  sem referência: rode patchd-server catalog sync -source terceiros")
	}
	if len(r.Findings) > 0 {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SITUAÇÃO\tPROGRAMA\tINSTALADA\tREFERÊNCIA\tORIGEM")
		for _, f := range r.Findings {
			name := f.Software.Name
			if f.Software.Scope == "user" {
				name += " (por usuário)"
			}
			installed := f.Installed
			if installed == "" {
				installed = fmt.Sprintf("%q", f.Software.Version)
			}
			ref, origin := f.Reference, ""
			switch {
			case f.Ref != nil:
				origin = fmt.Sprintf("%s, %s", f.Ref.Source, f.Ref.Fetched.Format("2006-01-02"))
				if now.Sub(f.Ref.Fetched) > 7*24*time.Hour {
					origin += " (antiga)"
				}
			case f.App.Category == thirdparty.CategoryMinimum:
				origin = "mínimo do manifesto"
			case f.App.Category == thirdparty.CategoryFeed:
				origin = f.App.Source.String()
			}
			if ref == "" {
				ref = "-"
			}
			if f.App.Note != "" && (f.State == thirdparty.StateEOL || f.State == thirdparty.StateOSVendor || verbose) {
				if origin != "" {
					origin += "; "
				}
				origin += f.App.Note
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.State, name, installed, ref, origin)
		}
		tw.Flush()
	}
	if verbose && len(r.Unmatched) > 0 {
		fmt.Fprintln(out, "\nSem regra no manifesto:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, s := range r.Unmatched {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, s.Version, s.Publisher)
		}
		tw.Flush()
	}
}
