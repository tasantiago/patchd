package patch

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/protocol"
)

// ErrNotImplemented indica uma busca ainda não implementada neste SO.
var ErrNotImplemented = errors.New("ainda não implementado neste SO")

// Identificadores das fontes de busca do Windows.
const (
	SourceWUADefault = "wua-default" // servidor definido pela política (WSUS quando configurado)
	SourceWUAOffline = "wua-offline" // catálogo wsusscn2.cab publicado pela Microsoft
)

// Options ajusta as buscas.
type Options struct {
	// OfflineCatalog é o caminho do wsusscn2.cab. Vazio: sem busca offline. Só no Windows.
	OfflineCatalog string
}

// Scanner busca atualizações. Cada SO tem sua implementação, escolhida na compilação;
// New devolve a do SO atual.
type Scanner interface {
	// Scan executa as buscas disponíveis e devolve um resultado por fonte. A falha de uma
	// fonte fica no próprio resultado e não impede as demais.
	Scan(ctx context.Context, opts Options) []protocol.ScanResult
	// History lista as últimas max operações registradas pelo SO, da mais recente para a mais antiga.
	History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error)
}

// unsupported é o resultado de um SO cuja busca ainda não existe. O catálogo offline,
// se pedido, é recusado com o motivo: ele só se aplica ao Windows.
func unsupported(source string, opts Options) []protocol.ScanResult {
	results := []protocol.ScanResult{{Source: source, Error: ErrNotImplemented.Error()}}
	if opts.OfflineCatalog != "" {
		results = append(results, protocol.ScanResult{
			Source: SourceWUAOffline,
			Error:  "o catálogo offline (wsusscn2.cab) só se aplica ao Windows",
		})
	}
	return results
}
