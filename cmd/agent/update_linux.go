package main

import (
	"errors"
	"os"
)

// startInstaller roda o install da versão nova numa unidade transitória do systemd. Um
// processo filho comum não serviria: ao parar o serviço, o systemd encerra todo o cgroup
// dele, inclusive o instalador que pediu a parada. A unidade transitória também fica fora
// do ProtectSystem=full do serviço, e pode gravar em /usr/local/sbin e /etc/systemd.
// A saída fica no journal: journalctl -u patchd-agent-update.
func startInstaller(bin string, args []string, dataDir string) error {
	systemdRun := ""
	for _, p := range []string{"/usr/bin/systemd-run", "/bin/systemd-run"} {
		if _, err := os.Stat(p); err == nil {
			systemdRun = p
			break
		}
	}
	if systemdRun == "" {
		return errors.New("systemd-run não encontrado")
	}
	cmd := append([]string{"--unit=" + serviceName + "-update", "--collect", "--quiet", "--", bin, "install"}, args...)
	return runTool(systemdRun, cmd...)
}
