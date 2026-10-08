package agent

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O anúncio do cache chega ao agente a cada check-in, antes da busca; sem anúncio, o
// agente recebe nil (para apagar a configuração gravada antes).
func TestCheckInEntregaOCacheDeRepositorios(t *testing.T) {
	for _, anunciado := range []bool{true, false} {
		var opts []api.Option
		if anunciado {
			opts = append(opts, api.WithRepoCache("http://patchd.exemplo:8080", []string{"archive.ubuntu.com"}))
		}
		srv, _, cred := novoServidor(t, opts...)
		var buscas atomic.Int32
		a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
		var ordem []string
		var recebido *protocol.RepoCache
		chamado := false
		a.RepoCache = func(_ context.Context, adv *protocol.RepoCache) {
			chamado, recebido = true, adv
			ordem = append(ordem, "cache")
		}
		scan := a.Scan
		a.Scan = func(ctx context.Context) protocol.PatchScanReport {
			ordem = append(ordem, "busca")
			return scan(ctx)
		}
		if out := a.CheckIn(context.Background()); out != outcomeOK {
			t.Fatalf("ciclo: %v", out)
		}
		switch {
		case !chamado:
			t.Errorf("anunciado=%v: o agente não foi avisado", anunciado)
		case anunciado && (recebido == nil || recebido.URL != "http://patchd.exemplo:8080"):
			t.Errorf("anúncio: %+v", recebido)
		case !anunciado && recebido != nil:
			t.Errorf("sem anúncio, recebeu %+v", recebido)
		}
		if strings.Join(ordem, ",") != "cache,busca" {
			t.Errorf("ordem: %v", ordem)
		}
	}
}
