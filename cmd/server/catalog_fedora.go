package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
)

// fedoraSource e fedoraStore são o que a sincronização do Fedora usa; os testes passam
// versões falsas.
type fedoraSource interface {
	Releases(ctx context.Context) ([]bodhi.Release, error)
	Updates(ctx context.Context, release string, since time.Time, page int) (bodhi.UpdatesPage, error)
}

type fedoraStore interface {
	FedoraLastPushed(ctx context.Context, release string) (time.Time, error)
	SaveFedoraUpdates(ctx context.Context, us []bodhi.Update) error
}

// fedoraOverlap recua o ponto de parada: um update publicado enquanto a página anterior
// era lida, ou com a data arredondada, entra de novo. Regravar o mesmo update não muda nada.
const fedoraOverlap = 24 * time.Hour

// maxFedoraPages é um teto de segurança: com 100 por página, a versão mais antiga em
// suporte não chega a 50 páginas. Um Bodhi que respondesse "pages" absurdo não prende o sync.
const maxFedoraPages = 500

// syncFedora sincroniza os updates de segurança estáveis das versões do Fedora em suporte.
// Por versão: sem nada guardado (ou com full), baixa tudo; senão, só os publicados desde a
// maior date_pushed guardada, menos fedoraOverlap. Cada versão é gravada numa única
// transação, só depois de todas as páginas chegarem: uma página que falha não deixa o
// ponto de parada avançar por cima do que faltou. Uma versão que falha não impede as outras.
func syncFedora(ctx context.Context, src fedoraSource, st fedoraStore, full bool, out io.Writer) (saved, failed int, err error) {
	releases, err := src.Releases(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("versões do Bodhi: %w", err)
	}
	if len(releases) == 0 {
		return 0, 0, fmt.Errorf("o Bodhi não listou nenhuma versão do Fedora em suporte")
	}
	names := make([]string, len(releases))
	for i, r := range releases {
		names[i] = r.Name
	}
	fmt.Fprintf(out, "  fedora: versões em suporte: %s\n", strings.Join(names, ", "))

	for _, r := range releases {
		start := time.Now()
		var since time.Time
		if !full {
			last, err := st.FedoraLastPushed(ctx, r.Name)
			if err != nil {
				return saved, failed, err
			}
			if !last.IsZero() {
				since = last.Add(-fedoraOverlap)
			}
		}

		var all []bodhi.Update
		skipped, pages := 0, 1
		var ferr error
		for page := 1; page <= pages; page++ {
			p, err := src.Updates(ctx, r.Name, since, page)
			if err != nil {
				ferr = fmt.Errorf("página %d: %w", page, err)
				break
			}
			if p.Pages > maxFedoraPages {
				ferr = fmt.Errorf("o Bodhi informou %d páginas (teto %d)", p.Pages, maxFedoraPages)
				break
			}
			pages = max(p.Pages, 1)
			skipped += p.SkippedBuilds
			for _, u := range p.Updates {
				// O filtro é do servidor; o que vier de outra versão é erro dele, não nosso.
				if u.Release != r.Name {
					ferr = fmt.Errorf("pedida a versão %s, veio o update %s da %s", r.Name, u.Alias, u.Release)
					break
				}
				all = append(all, u)
			}
			if ferr != nil {
				break
			}
		}
		if ferr == nil {
			ferr = st.SaveFedoraUpdates(ctx, all)
		}
		if ferr != nil {
			failed++
			fmt.Fprintf(out, "  %-4s FALHOU (nada gravado desta versão): %v\n", r.Name, ferr)
			continue
		}

		saved += len(all)
		mode := "completo"
		if !since.IsZero() {
			mode = "desde " + since.Format("2006-01-02 15:04") + " UTC"
		}
		fmt.Fprintf(out, "  %-4s %s: %d update(s) em %d página(s), %s", r.Name, mode, len(all), pages, time.Since(start).Round(100*time.Millisecond))
		if skipped > 0 {
			fmt.Fprintf(out, " (%d build(s) que não são rpm ignorados)", skipped)
		}
		fmt.Fprintln(out)
	}
	return saved, failed, nil
}
