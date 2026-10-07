package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/tasantiago/patchd/internal/thirdparty"
)

// thirdpartySource e thirdpartyStore são o que a sincronização dos programas de terceiros
// usa; os testes passam versões falsas.
type thirdpartySource interface {
	Latest(ctx context.Context, s thirdparty.Source) (string, error)
}

type thirdpartyStore interface {
	SaveThirdPartyVersion(ctx context.Context, app, osID, version, source string) error
	PruneThirdPartyVersions(ctx context.Context, keep []string) (int64, error)
}

// syncThirdParty busca a versão de referência de cada regra com fonte. Cada fonte é
// consultada uma vez por execução (o Firefox serve ao Windows e ao macOS). Uma fonte que
// falha não apaga a versão guardada: a próxima execução tenta de novo. As versões de regras
// que saíram do manifesto são apagadas.
func syncThirdParty(ctx context.Context, m thirdparty.Manifest, src thirdpartySource, st thirdpartyStore, out io.Writer) (saved, failed int, err error) {
	type result struct {
		version string
		err     error
	}
	cache := map[string]result{}
	keep := []string{} // não nil: a lista vazia apaga todas
	for _, a := range m.Apps {
		if a.Category != thirdparty.CategoryFeed || a.Source == nil {
			continue
		}
		keep = append(keep, a.Key())
		key := a.Source.String()
		r, ok := cache[key]
		if !ok {
			r.version, r.err = src.Latest(ctx, *a.Source)
			cache[key] = r
		}
		if r.err != nil {
			failed++
			fmt.Fprintf(out, "  %s FALHOU (fica a versão guardada): %v\n", a.Key(), r.err)
			continue
		}
		if err := st.SaveThirdPartyVersion(ctx, a.ID, a.OS, r.version, key); err != nil {
			return saved, failed, err
		}
		saved++
		fmt.Fprintf(out, "  %s: %s (%s)\n", a.Key(), r.version, key)
	}
	pruned, err := st.PruneThirdPartyVersions(ctx, keep)
	if err != nil {
		return saved, failed, err
	}
	fmt.Fprintf(out, "  terceiros: %d regra(s) com fonte, %d consulta(s), %d gravada(s), %d com falha, %d apagada(s) por sair do manifesto\n",
		len(keep), len(cache), saved, failed, pruned)
	return saved, failed, nil
}

// printThirdPartyRules escreve o "catalog terceiros": as regras do manifesto e a versão de
// referência guardada de cada uma.
func printThirdPartyRules(m thirdparty.Manifest, refs []thirdparty.Ref, out io.Writer) {
	byKey := map[string]thirdparty.Ref{}
	for _, r := range refs {
		byKey[r.App+"/"+r.OS] = r
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REGRA\tCATEGORIA\tFONTE OU MÍNIMO\tREFERÊNCIA\tBUSCADA EM (UTC)")
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
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Key(), a.Category, src, ref, when)
	}
	tw.Flush()
}
