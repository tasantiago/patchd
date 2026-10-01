package patch

import (
	"context"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// linuxScanner: provisório; apt e dnf entram na Aula 3.4.
type linuxScanner struct{ run platform.Runner }

// New devolve o scanner do SO atual.
func New(run platform.Runner) Scanner { return linuxScanner{run: run} }

func (linuxScanner) Scan(ctx context.Context, opts Options) []protocol.ScanResult {
	return unsupported("apt-dnf", opts)
}

func (linuxScanner) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	return nil, ErrNotImplemented
}
