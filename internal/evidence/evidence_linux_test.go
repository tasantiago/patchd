package evidence

import (
	"io/fs"
	"strings"
	"testing"
)

func arquivos(m map[string]any) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		switch v := m[p].(type) {
		case string:
			return []byte(v), nil
		case error:
			return nil, v
		default:
			return nil, fs.ErrNotExist
		}
	}
}

func TestCollectLinuxComoRoot(t *testing.T) {
	e, probs := collectLinux(arquivos(map[string]any{
		"/etc/machine-id":                  "4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b\n",
		"/sys/class/dmi/id/product_uuid":   "4c4c4544-0042-3510-8051-b7c04f4b4e32\n",
		"/sys/class/dmi/id/product_serial": "PF3ABC12\n",
	}))
	if e.InstallID != "4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b" || e.HardwareUUID != "4c4c4544-0042-3510-8051-b7c04f4b4e32" ||
		e.Serial != "PF3ABC12" || e.OSFamily != "linux" || len(probs) != 0 {
		t.Errorf("evidências: %+v %v", e, probs)
	}
}

func TestCollectLinuxSemRootEComDbus(t *testing.T) {
	e, probs := collectLinux(arquivos(map[string]any{
		"/var/lib/dbus/machine-id":         "aaaa0000000000000000000000000001",
		"/sys/class/dmi/id/product_uuid":   fs.ErrPermission,
		"/sys/class/dmi/id/product_serial": fs.ErrPermission,
	}))
	if e.InstallID != "aaaa0000000000000000000000000001" || e.HardwareUUID != "" || len(probs) != 2 ||
		!strings.Contains(probs[0], "permissão negada") {
		t.Errorf("sem root: install pelo dbus e dois avisos: %+v %v", e, probs)
	}
}

func TestCollectLinuxContainerSemNada(t *testing.T) {
	e, probs := collectLinux(arquivos(nil))
	if e.InstallID != "" || e.HardwareUUID != "" || len(probs) != 1 {
		t.Errorf("sem machine-id e sem DMI: só o aviso do machine-id: %+v %v", e, probs)
	}
}
