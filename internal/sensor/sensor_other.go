//go:build !windows

package sensor

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// For monta os sensores do Linux e do macOS (sem registro; serviço só com systemd).
func For(software func(ctx context.Context) ([]protocol.Software, error)) Sensors {
	s := Sensors{Software: software}
	if runtime.GOOS == "linux" {
		s.Processes = procProcesses
		if path := systemctl(); path != "" {
			s.Service = func(ctx context.Context, name string) (string, string, bool, error) {
				return systemdService(ctx, path, name)
			}
		}
	}
	return s
}

// procProcesses lê o /proc: o nome do executável (pelo link exe, sem o corte de 15 do comm).
func procProcesses() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		name := ""
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil {
			name = filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
		} else if b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")); err == nil { // #nosec G304 -- /proc/<pid>/comm
			name = strings.TrimSpace(string(b))
		}
		if name != "" {
			out = append(out, Process{Name: name})
		}
	}
	return out, nil
}

func systemctl() string {
	for _, p := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func systemdService(ctx context.Context, path, name string) (state, start string, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unit := name
	if !strings.Contains(unit, ".") {
		unit += ".service"
	}
	out, err := exec.CommandContext(ctx, path, "show", "--property=LoadState,ActiveState,UnitFileState", "--", unit).Output() // #nosec G204 -- caminho fixo; nome conferido (jobs.QueryParams)
	if err != nil {
		return "", "", false, err
	}
	props := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			props[k] = v
		}
	}
	if props["LoadState"] == "not-found" {
		return "", "", false, nil
	}
	state = map[string]string{"active": "rodando", "inactive": "parado", "failed": "falhou", "activating": "iniciando",
		"deactivating": "parando"}[props["ActiveState"]]
	if state == "" {
		state = props["ActiveState"]
	}
	start = map[string]string{"enabled": "início automático", "disabled": "desativado", "static": "estático", "masked": "bloqueado"}[props["UnitFileState"]]
	return state, start, true, nil
}
