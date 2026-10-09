//go:build !linux

package patch

import "github.com/tasantiago/patchd/internal/platform"

// NewInstaller: fora do Linux ainda não há job update (Windows: o WSUS instala, RF-30;
// macOS: mais adiante). nil faz o agente recusar o job, dizendo por quê.
func NewInstaller(platform.Runner) Installer { return nil }
