package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/tpexec"
	"github.com/tasantiago/patchd/internal/wintrust"
)

// newThirdParty liga o job thirdparty no Windows (Aula 14.4): o instalador vem do servidor
// para a pasta de dados do agente (só SYSTEM e administradores), o msiexec é o do sistema,
// por caminho absoluto, e cada instalação tem 30 minutos.
func newThirdParty(client *agent.Client, dataDir string) func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	e := &tpexec.Executor{
		Download: client.Download,
		Run:      platform.SilentRunner{Timeout: installTimeout},
		Verify:   wintrust.VerifyChain,
		Dir:      filepath.Join(dataDir, "pacotes"),
		LogDir:   filepath.Join(dataDir, "logs"),
		Msiexec:  filepath.Join(root, "System32", "msiexec.exe"),
	}
	return e.Install
}
