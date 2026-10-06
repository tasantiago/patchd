package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/store"
)

// kevSource e kevStore são o que a sincronização do KEV usa; os testes passam versões falsas.
type kevSource interface {
	Fetch(ctx context.Context) (kev.Catalog, error)
}

type kevStore interface {
	FeedHash(ctx context.Context, source string) (string, error)
	SaveKEV(ctx context.Context, c kev.Catalog) error
}

// syncKEV baixa o KEV (1,8 MB) e o substitui quando o dateReleased mudou. Um feed que não
// passa na conferência de integridade (kev.Parse) é recusado e o catálogo atual fica.
func syncKEV(ctx context.Context, src kevSource, st kevStore, out io.Writer) (changed, failed bool, err error) {
	c, ferr := src.Fetch(ctx)
	if ferr != nil {
		fmt.Fprintf(out, "  kev FALHOU (o catálogo atual fica): %v\n", ferr)
		return false, true, nil
	}
	prev, err := st.FeedHash(ctx, store.FeedKEV)
	if err != nil {
		return false, false, err
	}
	ransom := 0
	for _, v := range c.Vulns {
		if v.Ransomware {
			ransom++
		}
	}
	state := "em dia"
	if prev != c.Released {
		if err := st.SaveKEV(ctx, c); err != nil {
			return false, false, fmt.Errorf("kev: %w", err)
		}
		state, changed = "atualizado", true
	}
	fmt.Fprintf(out, "  kev: catálogo %s (%d CVEs, %d com uso em ransomware), %s\n", c.Version, len(c.Vulns), ransom, state)
	return changed, false, nil
}

// printKEV escreve o resumo do "catalog kev": a cobertura de cada fonte e as CVEs
// incluídas no KEV nos últimos dias, com onde cada uma aparece no catálogo.
func printKEV(info store.KEVSummary, cov []store.KEVCoverage, recent []store.KEVRecentVuln, days int, out io.Writer) {
	if info.Released == "" {
		fmt.Fprintln(out, "o KEV ainda não foi sincronizado: patchd-server catalog sync -source kev")
		return
	}
	fmt.Fprintf(out, "KEV: %d CVEs (%d com uso em ransomware), %d incluídas nos últimos 30 dias; liberado em %s\n\n",
		info.Total, info.Ransomware, info.Last30, info.Released)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FONTE\tCVEs\tNO KEV\tMARCADAS PELO FABRICANTE\tEXPLORADAS (KEV OU FABRICANTE)")
	for _, c := range cov {
		vendor := fmt.Sprint(c.Vendor)
		if c.Vendor < 0 {
			vendor = "não informa"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%d\n", c.Source, c.CVEs, c.InKEV, vendor, c.Exploited)
	}
	tw.Flush()

	fmt.Fprintf(out, "\nIncluídas no KEV nos últimos %d dias:\n", days)
	if len(recent) == 0 {
		fmt.Fprintln(out, "  nenhuma")
		return
	}
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CVE\tINCLUÍDA\tPRAZO\tFABRICANTE / PRODUTO\tRANSOMWARE\tONDE APARECE NO CATÁLOGO")
	for _, r := range recent {
		due := "-"
		if !r.Due.IsZero() {
			due = r.Due.Format("2006-01-02")
		}
		ransom := ""
		if r.Ransomware {
			ransom = "sim"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s / %s\t%s\t%s\n", r.CVE, r.Added.Format("2006-01-02"), due,
			r.Vendor, strings.TrimSpace(r.Product), ransom, where(r))
	}
	tw.Flush()
}

// where resume onde uma CVE do KEV aparece no catálogo.
func where(r store.KEVRecentVuln) string {
	var parts []string
	if len(r.MSRC) > 0 {
		parts = append(parts, "MSRC "+strings.Join(r.MSRC, ","))
	}
	if r.USNs > 0 {
		parts = append(parts, fmt.Sprintf("Ubuntu %d USN(s)", r.USNs))
	}
	if len(r.Fedora) > 0 {
		parts = append(parts, "Fedora "+strings.Join(r.Fedora, ","))
	}
	for _, inf := range r.Inferred {
		parts = append(parts, fmt.Sprintf("Fedora %s %s (inferido)", inf.Release, inf.NVR))
	}
	if len(r.Apple) > 0 {
		parts = append(parts, "macOS "+strings.Join(r.Apple, ","))
	}
	if len(parts) == 0 {
		return "fora do catálogo"
	}
	return strings.Join(parts, "; ")
}
