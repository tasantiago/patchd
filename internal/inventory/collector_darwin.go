package inventory

import (
	"context"
	"os"

	"github.com/tasantiago/patchd/internal/platform"
)

// darwinCollector coleta o inventário no macOS.
type darwinCollector struct {
	run platform.Runner
}

// New devolve o coletor do SO atual.
func New(run platform.Runner) Collector { return &darwinCollector{run: run} }

// OS lê o SystemVersion.plist via plutil (fonte de verdade da versão do macOS).
func (c *darwinCollector) OS(ctx context.Context) (OSInfo, error) {
	return collectDarwinOS(ctx, c.run, os.Hostname)
}
