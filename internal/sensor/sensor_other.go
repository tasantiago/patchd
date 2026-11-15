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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// For monta os sensores do Linux e do macOS (sem registro; serviço só com systemd).
func For(software func(ctx context.Context) ([]protocol.Software, error)) Sensors {
	s := Sensors{Software: software, Profiles: profiles, OS: runtime.GOOS}
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

// profiles lista as pastas dos usuários: no macOS, as de /Users; no Linux, as de /home, as
// contas de pessoa do /etc/passwd (UID a partir de 1000) e a do root.
func profiles() ([]Profile, error) {
	if runtime.GOOS == "darwin" {
		return homesIn("/Users", "Shared", "Guest"), nil
	}
	out := homesIn("/home", "lost+found")
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.Home] = true
	}
	add := func(user, home string) {
		home = filepath.Clean(home)
		if !seen[home] && filepath.IsAbs(home) && home != "/" {
			seen[home] = true
			out = append(out, Profile{User: user, Home: home})
		}
	}
	if f, err := os.Open("/etc/passwd"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// nome:senha:UID:GID:descrição:pasta:shell
			f := strings.Split(sc.Text(), ":")
			if len(f) < 7 {
				continue
			}
			if uid, err := strconv.Atoi(f[2]); err == nil && uid >= 1000 && uid < 65534 {
				add(f[0], f[5])
			}
		}
		f.Close()
	}
	add("root", "/root")
	return out, nil
}

// homesIn: as pastas de dir (menos as de skip e as ocultas), cada uma um usuário.
func homesIn(dir string, skip ...string) []Profile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Profile
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !plain(e.Type()) || strings.HasPrefix(name, ".") || slices.Contains(skip, name) {
			continue
		}
		out = append(out, Profile{User: name, Home: filepath.Join(dir, name)})
	}
	return out
}
