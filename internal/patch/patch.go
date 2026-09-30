package patch

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/protocol"
)

// ErrNotImplemented indica uma busca ainda não implementada neste SO.
var ErrNotImplemented = errors.New("ainda não implementado neste SO")

// Scanner busca atualizações na fonte padrão do SO. Cada SO tem sua implementação,
// escolhida na compilação; New devolve a do SO atual.
type Scanner interface {
	// Missing lista as atualizações aplicáveis e não instaladas.
	Missing(ctx context.Context) ([]protocol.MissingUpdate, error)
	// History lista as últimas max operações registradas pelo SO, da mais recente para a mais antiga.
	History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error)
}
