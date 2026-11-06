package sensor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// For monta os sensores do Windows. software: a lista do inventário.
func For(software func(ctx context.Context) ([]protocol.Software, error)) Sensors {
	return Sensors{
		Processes: func() ([]Process, error) {
			list, err := platform.Processes()
			out := make([]Process, 0, len(list))
			for _, p := range list {
				out = append(out, Process{Name: p.Name, Session: p.Session})
			}
			return out, err
		},
		Software: software, Service: service, Registry: readRegistry, FileVersion: fileVersion, Expand: expand,
		CaseInsensitive: true,
	}
}

// expand troca as pastas conhecidas (só as da lista do jobs.QueryParams).
func expand(p string) string {
	for _, v := range []struct{ token, env string }{
		{"%ProgramFiles(x86)%", "ProgramFiles(x86)"}, {"%ProgramFiles%", "ProgramFiles"}, {"%ProgramData%", "ProgramData"},
		{"%SystemRoot%", "SystemRoot"}, {"%SystemDrive%", "SystemDrive"},
	} {
		if len(p) >= len(v.token) && strings.EqualFold(p[:len(v.token)], v.token) {
			if val := os.Getenv(v.env); val != "" {
				return val + p[len(v.token):]
			}
		}
	}
	return p
}

func fileVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 || size > 1<<20 {
		return ""
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return ""
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil || fixed == nil || n == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff)
}

var serviceStates = map[svc.State]string{
	svc.Stopped: "parado", svc.StartPending: "iniciando", svc.StopPending: "parando", svc.Running: "rodando",
	svc.ContinuePending: "retomando", svc.PausePending: "pausando", svc.Paused: "pausado",
}

var startTypes = map[uint32]string{
	mgr.StartAutomatic: "início automático", mgr.StartManual: "início manual", mgr.StartDisabled: "desativado",
	windows.SERVICE_BOOT_START: "início no boot", windows.SERVICE_SYSTEM_START: "início do sistema",
}

func service(_ context.Context, name string) (state, start string, found bool, err error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", "", false, err
	}
	m := &mgr.Mgr{Handle: h}
	defer m.Disconnect()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", "", false, err
	}
	sh, err := windows.OpenService(h, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	s := &mgr.Service{Name: name, Handle: sh}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", "", true, err
	}
	state = serviceStates[st.State]
	if state == "" {
		state = fmt.Sprintf("estado %d", st.State)
	}
	if cfg, err := s.Config(); err == nil {
		start = startTypes[cfg.StartType]
		if cfg.DelayedAutoStart && cfg.StartType == mgr.StartAutomatic {
			start += " (atrasado)"
		}
	}
	return state, start, true, nil
}

func readRegistry(path, name string) (value string, keyFound, valueFound bool, err error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	defer k.Close()
	_, typ, err := k.GetValue(name, nil)
	if errors.Is(err, registry.ErrNotExist) {
		return "", true, false, nil
	}
	if err != nil && !errors.Is(err, registry.ErrShortBuffer) {
		return "", true, false, err
	}
	switch typ {
	case registry.SZ, registry.EXPAND_SZ:
		v, _, err := k.GetStringValue(name)
		return v, true, true, err
	case registry.DWORD, registry.QWORD:
		v, _, err := k.GetIntegerValue(name)
		return fmt.Sprint(v), true, true, err
	case registry.MULTI_SZ:
		v, _, err := k.GetStringsValue(name)
		return strings.Join(v, "; "), true, true, err
	default:
		n, _, _ := k.GetValue(name, nil)
		return fmt.Sprintf("binário (%d bytes)", n), true, true, nil
	}
}
