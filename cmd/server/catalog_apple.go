package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/apple"
	"github.com/tasantiago/patchd/internal/store"
)

// appleSource e appleStore são o que a sincronização da Apple usa; os testes passam versões
// falsas.
type appleSource interface {
	FetchGDMF(ctx context.Context) ([]apple.GDMFVersion, string, error)
	FetchSOFA(ctx context.Context) (apple.SOFAFeed, error)
}

type appleStore interface {
	FeedHash(ctx context.Context, source string) (string, error)
	SaveAppleVersions(ctx context.Context, vs []apple.GDMFVersion, hash string) error
	SaveSOFA(ctx context.Context, f apple.SOFAFeed) error
	AppleMajors(ctx context.Context) ([]store.AppleMajor, error)
}

// sofaVanishWindow: uma major com versão de segurança nos últimos 365 dias não pode sumir do
// SOFA de uma hora para outra; se sumir, o feed está quebrado.
const sofaVanishWindow = 365 * 24 * time.Hour

// syncApple baixa as duas fontes (pequenas, sem pedido condicional) e grava cada uma só
// quando o conteúdo mudou: o GDMFHash (conteúdo reduzido e ordenado) no gdmf, o UpdateHash
// no SOFA. Uma resposta vazia é recusada: um feed quebrado não pode apagar o catálogo que
// está no banco. Uma fonte que falha não impede a outra.
func syncApple(ctx context.Context, src appleSource, st appleStore, out io.Writer) (changed, failed int, err error) {
	// gdmf: o que a Apple publica para instalar.
	vs, hash, ferr := src.FetchGDMF(ctx)
	if ferr == nil && len(vs) == 0 {
		ferr = fmt.Errorf("resposta sem nenhuma versão do macOS; o catálogo atual fica")
	}
	if ferr != nil {
		failed++
		fmt.Fprintf(out, "  gdmf FALHOU: %v\n", ferr)
	} else {
		bsi := 0
		for _, v := range vs {
			if v.Extra != "" {
				bsi++
			}
		}
		state, err := saveIfChanged(ctx, st, store.FeedAppleGDMF, hash, func() error { return st.SaveAppleVersions(ctx, vs, hash) })
		if err != nil {
			return changed, failed, err
		}
		if state == "atualizado" {
			changed++
		}
		fmt.Fprintf(out, "  gdmf: %d versões publicadas do macOS (%d melhorias em segundo plano), %s\n", len(vs)-bsi, bsi, state)
	}

	// SOFA: as versões de segurança, com as CVEs.
	f, ferr := src.FetchSOFA(ctx)
	if ferr == nil && (len(f.Releases) == 0 || f.UpdateHash == "") {
		ferr = fmt.Errorf("feed sem versões ou sem UpdateHash; o catálogo atual fica")
	}
	if ferr != nil {
		failed++
		fmt.Fprintf(out, "  sofa FALHOU: %v\n", ferr)
		return changed, failed, nil
	}
	majors := map[string]bool{}
	cves, exploited := 0, map[string]bool{}
	for _, r := range f.Releases {
		majors[r.MajorNumber] = true
	}
	// A gravação do SOFA substitui tudo: uma major que sumisse do feed sumiria do catálogo, e a
	// avaliação dos Macs nela mudaria (fim de suporte contado pela posição, Aula 6.4). Major
	// recente que some: o feed é recusado. Major antiga (sem versão há mais de um ano): sai,
	// com aviso.
	stored, err := st.AppleMajors(ctx)
	if err != nil {
		return changed, failed, err
	}
	for _, m := range stored {
		if majors[m.Number] {
			continue
		}
		if time.Since(m.Date) < sofaVanishWindow {
			failed++
			fmt.Fprintf(out, "  sofa RECUSADO: a major %s (%s, última versão em %s) sumiu do feed; o catálogo atual fica\n",
				m.Number, m.Major, m.Date.Format("2006-01-02"))
			return changed, failed, nil
		}
		fmt.Fprintf(out, "  sofa: a major %s (%s, última versão em %s) saiu do feed e sai do catálogo\n",
			m.Number, m.Major, m.Date.Format("2006-01-02"))
	}
	for _, r := range f.Releases {
		cves += len(r.CVEs)
		for _, c := range r.CVEs {
			if c.Exploited {
				exploited[c.ID] = true
			}
		}
	}
	state, err := saveIfChanged(ctx, st, store.FeedAppleSOFA, f.UpdateHash, func() error { return st.SaveSOFA(ctx, f) })
	if err != nil {
		return changed, failed, err
	}
	if state == "atualizado" {
		changed++
	}
	fmt.Fprintf(out, "  sofa: %d majors, %d versões de segurança, %d CVEs (%d exploradas distintas), %d modelos de Mac, %s\n",
		len(majors), len(f.Releases), cves, len(exploited), len(f.Models), state)
	return changed, failed, nil
}

// saveIfChanged grava só quando o hash difere do guardado.
func saveIfChanged(ctx context.Context, st appleStore, source, hash string, save func() error) (string, error) {
	prev, err := st.FeedHash(ctx, source)
	if err != nil {
		return "", err
	}
	if prev == hash {
		return "em dia", nil
	}
	if err := save(); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return "atualizado", nil
}
