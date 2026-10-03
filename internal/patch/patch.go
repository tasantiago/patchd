package patch

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/protocol"
)

// ErrNotImplemented indica uma busca ainda não implementada neste SO.
var ErrNotImplemented = errors.New("ainda não implementado neste SO")

// Identificadores das fontes de busca.
const (
	SourceWUADefault = "wua-default" // Windows: servidor definido pela política (WSUS quando configurado)
	SourceWUAOffline = "wua-offline" // Windows: catálogo wsusscn2.cab publicado pela Microsoft
	SourceApt        = "apt"         // Ubuntu/Debian: simulação do apt-get
	SourceDnf        = "dnf"         // Fedora/RHEL: advisories de segurança do dnf
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
	// Reboot informa se há algo instalado esperando reinício, sinal por sinal.
	Reboot(ctx context.Context) (protocol.RebootStatus, error)
	// History lista as últimas max operações registradas pelo SO, da mais recente para a mais antiga.
	History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error)
}

// offlineNotWindows recusa o catálogo offline fora do Windows, com o motivo.
func offlineNotWindows() protocol.ScanResult {
	return protocol.ScanResult{
		Source: SourceWUAOffline,
		Error:  "o catálogo offline (wsusscn2.cab) só se aplica ao Windows",
	}
}

// unsupported é o resultado de um SO cuja busca ainda não existe.
func unsupported(source string, opts Options) []protocol.ScanResult {
	results := []protocol.ScanResult{{Source: source, Error: ErrNotImplemented.Error()}}
	if opts.OfflineCatalog != "" {
		results = append(results, offlineNotWindows())
	}
	return results
}
