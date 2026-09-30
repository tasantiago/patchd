package inventory

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/platform"
)

// windowsCollector coleta o inventário no Windows.
type windowsCollector struct {
	run platform.Runner
}

// New devolve o coletor do SO atual.
func New(run platform.Runner) Collector { return &windowsCollector{run: run} }

// OS: provisório; a implementação pelo registro vem na próxima etapa da Aula 2.1.
func (c *windowsCollector) OS(ctx context.Context) (OSInfo, error) {
	return OSInfo{}, errors.New("identificação do SO no Windows ainda não implementada")
}
