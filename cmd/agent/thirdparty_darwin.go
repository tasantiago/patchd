package main

import (
	"context"
	"path/filepath"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/macinstall"
)

// newThirdParty liga o job thirdparty no macOS: o Installomator, que o servidor entrega preso
// pelo SHA-256 (internal/installomator), roda como root com o rótulo do programa. O script fica
// na pasta de dados do agente (só root) e a saída em logs/installomator-job-N.log.
func newThirdParty(client *agent.Client, dataDir string, _ []string) func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	e := &macinstall.Executor{
		Download: client.Download,
		Dir:      filepath.Join(dataDir, "installomator"),
		LogDir:   filepath.Join(dataDir, "logs"),
		Shell:    "/bin/zsh",
	}
	return e.Install
}
