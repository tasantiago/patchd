//go:build !linux && !windows

package main

import (
	"context"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
)

// newRestart: no macOS o patchd ainda não reinicia (sem Mac no laboratório; Aula 8.7).
// nil faz o agente recusar o job, dizendo por quê.
func newRestart(platform.Runner) func(ctx context.Context, jobID int64, delay time.Duration) error {
	return nil
}

// newAbortRestart: sem reinício pelo patchd, nada a cancelar.
func newAbortRestart(platform.Runner) func(ctx context.Context) error { return nil }
