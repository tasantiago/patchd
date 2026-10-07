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

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/version"
)

const complianceUsage = `uso: patchd-server compliance -machine ID [-v]

Avalia o inventário atual de uma máquina contra o catálogo: os pacotes fonte com correção
pendente, o kernel em execução e as CVEs exploradas (KEV). ID pode ser o começo do ID da
máquina, desde que case com uma só. -v lista os avisos pendentes de cada pacote.

Nesta versão: Ubuntu e Fedora. Windows e macOS entram nas próximas partes da Aula 6.4.
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
}

// machineEval é a avaliação de uma máquina, pronta para imprimir.
type machineEval struct {
	ID        string
	OS        protocol.OSInfo
	Collected time.Time
	Catalog   string // ecossistema (Ubuntu) ou versão (Fedora) usada no catálogo
	Linux     *compliance.LinuxResult
	Note      string // por que não houve avaliação (SO ainda não suportado, versão fora do catálogo)
}

var errNoMachine = errors.New("nenhuma máquina com esse ID")

func runCompliance(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compliance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	machine := fs.String("machine", "", "ID (ou começo do ID) da máquina")
	verbose := fs.Bool("v", false, "lista os avisos pendentes de cada pacote")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *machine == "" {
		fmt.Fprint(stderr, complianceUsage)
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

	ev, err := evaluateMachine(ctx, store.NewPostgres(pool), *machine)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	printEval(ev, *verbose, time.Now(), stdout)
	return exitOK
}

// evaluateMachine carrega o inventário atual e o avalia contra o catálogo da distribuição.
func evaluateMachine(ctx context.Context, st complianceStore, prefix string) (machineEval, error) {
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
	default:
		ev.Note = fmt.Sprintf("a avaliação de %s %s entra nas próximas partes da Aula 6.4", inv.OS.Name, inv.OS.Version)
	}
	return ev, nil
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
