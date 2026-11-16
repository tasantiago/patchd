package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

// thirdpartySource e thirdpartyStore são o que a sincronização dos programas de terceiros
// usa; os testes passam versões falsas.
type thirdpartySource interface {
	Latest(ctx context.Context, s thirdparty.Source) (string, error)
	Lifecycle(ctx context.Context, product string) ([]thirdparty.Cycle, error)
}

type thirdpartyStore interface {
	SaveThirdPartyVersion(ctx context.Context, app, osID, version, source string) error
	SaveThirdPartyCheck(ctx context.Context, app, osID, version, source string) error
	PruneThirdPartyVersions(ctx context.Context, keep []string) (int64, error)
	ReplaceLifecycle(ctx context.Context, product string, cycles []thirdparty.Cycle) error
	PruneLifecycles(ctx context.Context, keep []string) (int64, error)
}

// syncThirdParty busca a versão de referência de cada regra com fonte. Cada fonte é
// consultada uma vez por execução (o Firefox serve ao Windows e ao macOS). Uma fonte que
// falha não apaga a versão guardada: a próxima execução tenta de novo. As versões de regras
// que saíram do manifesto são apagadas.
func syncThirdParty(ctx context.Context, m thirdparty.Manifest, src2 thirdpartySource, st thirdpartyStore, out io.Writer) (saved, failed int, err error) {
	type result struct {
		version string
		err     error
	}
	cache := map[string]result{}
	latest := func(src thirdparty.Source) result {
		key := src.String()
		r, ok := cache[key]
		if !ok {
			r.version, r.err = src2.Latest(ctx, src)
			cache[key] = r
		}
		return r
	}
	keep := []string{} // não nil: a lista vazia apaga todas
	for _, a := range m.Apps {
		if a.Category != thirdparty.CategoryFeed || a.Source == nil {
			continue
		}
		keep = append(keep, a.Key())
		key := a.Source.String()
		r := latest(*a.Source)
		if r.err != nil {
			failed++
			fmt.Fprintf(out, "  %s FALHOU (fica a versão guardada): %v\n", a.Key(), r.err)
			continue
		}
		if err := st.SaveThirdPartyVersion(ctx, a.ID, a.OS, r.version, key); err != nil {
			return saved, failed, err
		}
		saved++
		line := fmt.Sprintf("  %s: %s (%s)", a.Key(), r.version, key)
		// A segunda fonte: a falha dela não derruba a principal; a conferência anterior fica.
		if a.Check != nil {
			c := latest(*a.Check)
			switch {
			case c.err != nil:
				failed++
				line += fmt.Sprintf("; conferência %s FALHOU: %v", a.Check, c.err)
			default:
				if err := st.SaveThirdPartyCheck(ctx, a.ID, a.OS, c.version, a.Check.String()); err != nil {
					return saved, failed, err
				}
				line += fmt.Sprintf("; conferência %s (%s)", c.version, a.Check)
				if n, err := version.CompareDotted(c.version, r.version); err == nil && n > 0 {
					line += " · a fonte principal ainda não acompanhou"
				}
			}
		}
		fmt.Fprintln(out, line)
	}
	pruned, err := st.PruneThirdPartyVersions(ctx, keep)
	if err != nil {
		return saved, failed, err
	}
	// Os ciclos de suporte (endoflife.date): um pedido por produto.
	products := []string{}
	for _, a := range m.Apps {
		if a.Lifecycle != "" && !slices.Contains(products, a.Lifecycle) {
			products = append(products, a.Lifecycle)
		}
	}
	cycles := 0
	for _, p := range products {
		cs, err := src2.Lifecycle(ctx, p)
		if err != nil {
			failed++
			fmt.Fprintf(out, "  ciclos de %s (endoflife.date) FALHOU (ficam os guardados): %v\n", p, err)
			continue
		}
		if err := st.ReplaceLifecycle(ctx, p, cs); err != nil {
			return saved, failed, err
		}
		cycles += len(cs)
		fmt.Fprintf(out, "  ciclos de %s (endoflife.date): %d\n", p, len(cs))
	}
	if _, err := st.PruneLifecycles(ctx, products); err != nil {
		return saved, failed, err
	}
	fmt.Fprintf(out, "  terceiros: %d regra(s) com fonte, %d consulta(s), %d gravada(s), %d com falha, %d apagada(s) por sair do manifesto; %d produto(s) com ciclos de suporte (%d ciclos)\n",
		len(keep), len(cache), saved, failed, pruned, len(products), cycles)
	return saved, failed, nil
}

// usesInstallomator: alguma regra do macOS instala pelo Installomator.
func usesInstallomator(m thirdparty.Manifest) bool {
	for _, a := range m.For("macos") {
		if a.Install != nil && a.Install.Installomator != "" {
			return true
		}
	}
	return false
}

// syncInstallomator garante o script dos Macs na pasta de pacotes, quando alguma regra o usa.
// false: falhou (a sincronização dos terceiros fica com falha, e o script guardado continua).
func syncInstallomator(ctx context.Context, srcs catalogSources, out io.Writer) bool {
	if !usesInstallomator(srcs.Manifest) || srcs.Installomator == nil && srcs.InstallomatorErr == nil {
		return true
	}
	switch {
	case srcs.InstallomatorErr != nil:
		fmt.Fprintf(out, "  Installomator FALHOU: %v\n", srcs.InstallomatorErr)
		return false
	case srcs.ContentDir == "":
		fmt.Fprintln(out, "  Installomator: sem PATCHD_CONTENT_DIR, os Macs ficam sem instalação pelo patchd")
		return true
	}
	n, cached, err := srcs.Installomator(ctx, packagesDir(srcs.ContentDir))
	switch {
	case err != nil:
		fmt.Fprintf(out, "  Installomator %s FALHOU (fica o guardado, se houver): %v\n", srcs.InstallomatorVer, err)
		return false
	case cached:
		fmt.Fprintf(out, "  Installomator %s: no cache, conferido (%d bytes)\n", srcs.InstallomatorVer, n)
	default:
		fmt.Fprintf(out, "  Installomator %s: baixado e conferido (%d bytes)\n", srcs.InstallomatorVer, n)
	}
	return true
}

// printThirdPartyRules escreve o "catalog terceiros": as regras do manifesto e a versão de
// referência guardada de cada uma.
func printThirdPartyRules(m thirdparty.Manifest, refs []thirdparty.Ref, out io.Writer) {
	byKey := map[string]thirdparty.Ref{}
	for _, r := range refs {
		byKey[r.App+"/"+r.OS] = r
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REGRA\tCATEGORIA\tFONTE OU MÍNIMO\tREFERÊNCIA\tBUSCADA EM (UTC)\tCONFERÊNCIA\tCICLOS")
	for _, a := range m.Apps {
		src, ref, when := "-", "-", "-"
		switch {
		case a.Source != nil:
			src = a.Source.String()
			if r, ok := byKey[a.Key()]; ok {
				ref, when = r.Version, r.Fetched.Format("2006-01-02 15:04")
			} else {
				ref = "(sem sincronizar)"
			}
		case a.MinVersion != "":
			src, ref = "mínimo", a.MinVersion
		case a.Category == thirdparty.CategoryLifecycle:
			src, ref = "endoflife.date", "a mais nova do ciclo"
		}
		check, life := "-", "-"
		if a.Check != nil {
			check = a.Check.String()
			if r, ok := byKey[a.Key()]; ok && r.CheckVersion != "" {
				check += " " + r.CheckVersion
			}
		}
		if a.Lifecycle != "" {
			life = "endoflife.date:" + a.Lifecycle
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Key(), a.Category, src, ref, when, check, life)
	}
	tw.Flush()
}
