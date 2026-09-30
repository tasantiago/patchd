package inventory

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/platform"
)

// windowsCollector coleta o inventário no Windows.
type windowsCollector struct {
	run platform.Runner
}

// New devolve o coletor do SO atual.
func New(run platform.Runner) Collector { return &windowsCollector{run: run} }

// OS lê a versão do Windows no registro.
func (c *windowsCollector) OS(ctx context.Context) (OSInfo, error) {
	cv, err := readCurrentVersion()
	if err != nil {
		return OSInfo{Family: "windows"}, err
	}
	hostname, _ := os.Hostname()
	return windowsOSInfo(cv, hostname), nil
}

// readCurrentVersion lê HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion.
// WOW64_64KEY garante a visão de 64 bits mesmo se o processo for de 32 bits.
func readCurrentVersion() (currentVersion, error) {
	var cv currentVersion
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return cv, fmt.Errorf("abrir %s: %w", currentVersionKey, err)
	}
	defer k.Close()

	cv.CurrentBuild, _, err = k.GetStringValue("CurrentBuild")
	if err != nil {
		return cv, fmt.Errorf("ler CurrentBuild: %w", err)
	}
	// Opcionais: ausentes em versões antigas ou edições específicas.
	cv.UBR, _, _ = k.GetIntegerValue("UBR")
	cv.DisplayVersion, _, _ = k.GetStringValue("DisplayVersion")
	cv.EditionID, _, _ = k.GetStringValue("EditionID")
	return cv, nil
}
