package patch

import (
	"context"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Installer aplica os pacotes de um job update (Aula 8.4). O erro que satisfaz
// errors.Is(err, jobs.ErrNotPending) é recusa (o pedido não bate com a busca desta
// máquina); qualquer outro é falha da instalação.
type Installer interface {
	Install(ctx context.Context, jobID int64, pkgs []jobs.Package) (detail string, err error)
}
