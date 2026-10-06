package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/osv"
)

// ubuntuSource e ubuntuStore são o que a sincronização do Ubuntu usa; os testes passam
// versões falsas.
type ubuntuSource interface {
	ModifiedIDs(ctx context.Context, prefix string) ([]osv.Entry, error)
	Record(ctx context.Context, id string) (osv.Record, error)
}

type ubuntuStore interface {
	UbuntuVersions(ctx context.Context) (map[string]string, error)
	SaveUbuntuUSN(ctx context.Context, r osv.Record, modified string) error
}

// ubuntuResult resume uma sincronização.
type ubuntuResult struct {
	Listed    int // USNs na lista do OSV
	Pending   int // novas ou com data diferente da guardada
	Saved     int
	Withdrawn int // retiradas pelo Ubuntu: guardadas sem correções, fora das consultas
	Failed    int
}

// maxShownFailures limita as falhas impressas: numa primeira carga com a rede instável,
// milhares de linhas iguais esconderiam o resumo.
const maxShownFailures = 10

// syncUbuntu baixa o modified_id.csv do Ubuntu e, das USNs, só as novas ou com data
// diferente da guardada, na ordem da lista (as mais recentes primeiro). limit > 0 limita
// quantas são baixadas nesta execução; o resto fica para a próxima. Os downloads correm
// em paralelo (workers); cada USN é gravada na sua própria transação, e uma que falha não
// impede as outras. Uma USN retirada é gravada como retirada.
func syncUbuntu(ctx context.Context, src ubuntuSource, st ubuntuStore, limit, workers int, out io.Writer) (ubuntuResult, error) {
	var res ubuntuResult
	list, err := src.ModifiedIDs(ctx, "USN-")
	if err != nil {
		return res, fmt.Errorf("lista do OSV (Ubuntu): %w", err)
	}
	known, err := st.UbuntuVersions(ctx)
	if err != nil {
		return res, err
	}
	res.Listed = len(list)

	var todo []osv.Entry
	for _, e := range list {
		if known[e.ID] != e.Modified {
			todo = append(todo, e)
		}
	}
	res.Pending = len(todo)
	if limit > 0 && len(todo) > limit {
		todo = todo[:limit]
	}
	fmt.Fprintf(out, "  ubuntu: %d USNs na lista, %d novas ou alteradas, baixando %d\n", res.Listed, res.Pending, len(todo))
	if len(todo) == 0 {
		return res, nil
	}

	var (
		mu    sync.Mutex
		done  int
		start = time.Now()
		jobs  = make(chan osv.Entry)
		wg    sync.WaitGroup
	)
	report := func(e osv.Entry, withdrawn bool, err error) {
		mu.Lock()
		defer mu.Unlock()
		done++
		switch {
		case err != nil:
			res.Failed++
			if res.Failed <= maxShownFailures {
				fmt.Fprintf(out, "  %s FALHOU: %v\n", e.ID, err)
			}
		case withdrawn:
			res.Withdrawn++
		default:
			res.Saved++
		}
		if done%500 == 0 {
			fmt.Fprintf(out, "  ubuntu: %d/%d em %s\n", done, len(todo), time.Since(start).Round(time.Second))
		}
	}

	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				r, err := src.Record(ctx, e.ID)
				if err != nil {
					report(e, false, err)
					continue
				}
				// A retirada também é gravada (sem correções): sem a linha guardada, ela
				// voltaria como "nova" na próxima execução.
				report(e, !r.Withdrawn.IsZero(), st.SaveUbuntuUSN(ctx, r, e.Modified))
			}
		}()
	}
	for _, e := range todo {
		select {
		case jobs <- e:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	if res.Failed > maxShownFailures {
		fmt.Fprintf(out, "  ... e mais %d falhas\n", res.Failed-maxShownFailures)
	}
	fmt.Fprintf(out, "  ubuntu: %d gravadas, %d retiradas, %d com falha, em %s\n",
		res.Saved, res.Withdrawn, res.Failed, time.Since(start).Round(100*time.Millisecond))
	if ctx.Err() != nil {
		return res, fmt.Errorf("sincronização do Ubuntu interrompida: %w", ctx.Err())
	}
	return res, nil
}
