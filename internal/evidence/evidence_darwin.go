package evidence

import (
	"context"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

const systemProfilerPath = "/usr/sbin/system_profiler"

// collectOS: o macOS não tem um identificador documentado da instalação (o equivalente
// ao MachineGuid). Vão o platform_UUID e o serial, que são do hardware e sobrevivem à
// reinstalação. Consequência: no Mac, o servidor não distingue "agente reinstalado" de
// "macOS reinstalado"; os dois viram same_hardware.
func collectOS(ctx context.Context, run platform.Runner) (protocol.IdentityEvidence, []string) {
	e := protocol.IdentityEvidence{OSFamily: "darwin"}
	out, err := run.Run(ctx, systemProfilerPath, "SPHardwareDataType", "-json")
	if err != nil {
		return e, []string{"SPHardwareDataType: " + err.Error()}
	}
	uuid, serial, err := parseHardwareIdentity(out)
	if err != nil {
		return e, []string{err.Error()}
	}
	e.HardwareUUID, e.Serial = uuid, serial
	return e, nil
}
