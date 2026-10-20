//go:build !windows

package main

import (
	"context"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/jobs"
)

// newThirdParty: fora do Windows o patchd não instala programas de terceiros (no Linux
// eles vêm dos repositórios, pelo job update; o macOS fica para o MDM). nil faz o agente
// recusar o job, dizendo por quê.
func newThirdParty(*agent.Client, string, []string) func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	return nil
}
