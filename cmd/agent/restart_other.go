//go:build !linux

package main

import (
	"context"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
)

// newRestart: fora do Linux o patchd ainda não reinicia (Windows: a GPO do WSUS cuida,
// RF-30; macOS: mais adiante). nil faz o agente recusar o job, dizendo por quê.
func newRestart(platform.Runner) func(ctx context.Context, jobID int64, delay time.Duration) error {
	return nil
}
