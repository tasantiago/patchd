package inventory

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/platform"
)

// darwinCollector coleta o inventário no macOS.
type darwinCollector struct {
	run platform.Runner
}

// New devolve o coletor do SO atual.
func New(run platform.Runner) Collector { return &darwinCollector{run: run} }

// OS: provisório; a implementação pelo SystemVersion.plist vem na próxima etapa da Aula 2.1.
func (c *darwinCollector) OS(ctx context.Context) (OSInfo, error) {
	return OSInfo{}, errors.New("identificação do SO no macOS ainda não implementada")
}
