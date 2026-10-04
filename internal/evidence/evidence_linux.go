package evidence

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Fontes no Linux. O /var/lib/dbus/machine-id é o mesmo valor em distribuições antigas.
var (
	machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	dmiUUIDPath    = "/sys/class/dmi/id/product_uuid"
	dmiSerialPath  = "/sys/class/dmi/id/product_serial"
)

func collectOS(ctx context.Context, run platform.Runner) (protocol.IdentityEvidence, []string) {
	return collectLinux(os.ReadFile)
}

// collectLinux recebe a leitura de arquivos para ser testável sem a máquina real.
func collectLinux(readFile func(string) ([]byte, error)) (protocol.IdentityEvidence, []string) {
	e := protocol.IdentityEvidence{OSFamily: "linux"}
	var problems []string

	for _, p := range machineIDPaths {
		if v, err := readFile(p); err == nil && strings.TrimSpace(string(v)) != "" {
			e.InstallID = strings.TrimSpace(string(v))
			break
		}
	}
	if e.InstallID == "" {
		problems = append(problems, "machine-id ausente em /etc/machine-id e /var/lib/dbus/machine-id")
	}

	read := func(path, name string) string {
		v, err := readFile(path)
		switch {
		case err == nil:
			return strings.TrimSpace(string(v))
		case errors.Is(err, fs.ErrPermission):
			problems = append(problems, name+": permissão negada (o agente roda como root)")
		case errors.Is(err, fs.ErrNotExist):
			// Sem DMI (ARM, alguns ambientes virtuais): evidência ausente, não é erro.
		default:
			problems = append(problems, name+": "+err.Error())
		}
		return ""
	}
	e.HardwareUUID = read(dmiUUIDPath, "product_uuid")
	e.Serial = read(dmiSerialPath, "product_serial")
	return e, problems
}
