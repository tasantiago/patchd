package patch

import (
	"context"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// darwinScanner: provisório; softwareupdate entra na Aula 3.5.
type darwinScanner struct{ run platform.Runner }

// New devolve o scanner do SO atual.
func New(run platform.Runner) Scanner { return darwinScanner{run: run} }

func (darwinScanner) Scan(ctx context.Context, opts Options) []protocol.ScanResult {
	return unsupported("softwareupdate", opts)
}

func (darwinScanner) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	return nil, ErrNotImplemented
}
