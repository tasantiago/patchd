package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/tpexec"
	"github.com/tasantiago/patchd/internal/wintrust"
)

// newThirdParty liga o job thirdparty no Windows (Aula 14.4): o instalador vem do servidor
// para a pasta de dados do agente (só SYSTEM e administradores), o msiexec é o do sistema,
// por caminho absoluto, e cada instalação tem 60 minutos (90 para instaladores de 200 MB ou
// mais: tpexec.TimeoutFor). O log do msiexec e o do Inno ficam em logs\instalar-job-N.log.
func newThirdParty(client *agent.Client, dataDir string, allowed []string) func(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	e := &tpexec.Executor{
		Download: client.Download,
		// O prazo de cada instalação vem do executor (pelo tamanho); o do runner é só o teto.
		Run:     platform.SilentRunner{Timeout: tpexec.LargeInstallTimeout + 10*time.Minute},
		Verify:  wintrust.VerifyChain,
		Dir:     filepath.Join(dataDir, "pacotes"),
		LogDir:  filepath.Join(dataDir, "logs"),
		Msiexec: filepath.Join(root, "System32", "msiexec.exe"),
		// Aula 14.6b: com o programa aberto, o job não roda.
		Processes: platform.Processes,
		// Aula 20.5b: a lista de organizações da própria máquina.
		AllowedSigners: allowed,
		// Aula 20.6: o instalador fica seguro da conferência ao fim da execução, e o TEMP
		// dele é uma pasta só do SYSTEM.
		Hold:    platform.HoldFile,
		TempDir: filepath.Join(dataDir, "tmp"),
		// Outra instalação MSI em andamento: o job volta "adiado" na hora, sem esperar.
		InstallerBusy: platform.WindowsInstallerBusy,
	}
	return e.Install
}
