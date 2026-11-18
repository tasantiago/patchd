//go:build !windows && !darwin

package main

import (
	"context"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/jobs"
)

// newThirdParty: no Linux o patchd não instala programas de terceiros (eles vêm dos
// repositórios, pelo job update). nil faz o agente recusar o job, dizendo por quê.
func newThirdParty(*agent.Client, string, []string) func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	return nil
}

// newUninstall: só o Windows remove programas pelo patchd.
func newUninstall(string) func(ctx context.Context, jobID int64, p jobs.UninstallParams) (string, error) {
	return nil
}
