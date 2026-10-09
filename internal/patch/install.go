package patch

import (
	"context"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Installer aplica os pacotes de um job update (Aula 8.4). O erro que satisfaz
// errors.Is(err, jobs.ErrNotPending) é recusa (o pedido não bate com a busca desta
// máquina); qualquer outro é falha da instalação.
type Installer interface {
	Install(ctx context.Context, jobID int64, pkgs []jobs.Package) (detail string, err error)
}

// lastLines devolve as últimas n linhas não vazias, para o detalhe do resultado.
func lastLines(out []byte, n int) string {
	var keep []string
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{l}, keep...)
		}
	}
	s := strings.Join(keep, " | ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
