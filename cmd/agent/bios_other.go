//go:build !windows

package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/jobs"
)

// biosRunner: fora do Windows, o agente não grava a BIOS (Aula 21.5).
func biosRunner(*slog.Logger, []byte) func(context.Context, int64, jobs.BIOSPasswordParams) (string, error) {
	return nil
}
