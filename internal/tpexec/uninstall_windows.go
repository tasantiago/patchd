//go:build windows

package tpexec

import (
	"errors"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Prefixos das chaves Uninstall do HKLM, com a visão do registro de cada uma.
var uninstallViews = []struct {
	prefix, path string
	access       uint32
}{
	{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`, registry.WOW64_64KEY},
	{`HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\`, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`, registry.WOW64_32KEY},
}

// LookupUninstall lê a entrada Uninstall pela chave do job (o Source do inventário).
func LookupUninstall(key string) (UninstallEntry, bool, error) {
	sub := jobs.ReplaceEntry{Key: key}.ReplaceSubkey()
	if sub == "" {
		return UninstallEntry{}, false, errors.New("chave fora das chaves Uninstall do HKLM")
	}
	for _, v := range uninstallViews {
		if !strings.HasPrefix(key, v.prefix) {
			continue
		}
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, v.path+sub, registry.QUERY_VALUE|v.access)
		if errors.Is(err, registry.ErrNotExist) {
			return UninstallEntry{}, false, nil
		}
		if err != nil {
			return UninstallEntry{}, false, err
		}
		defer k.Close()
		get := func(name string) string {
			s, _, err := k.GetStringValue(name)
			if err != nil {
				return ""
			}
			return s
		}
		return UninstallEntry{DisplayName: get("DisplayName"), DisplayVersion: get("DisplayVersion"),
			QuietUninstall: get("QuietUninstallString"), UninstallLine: get("UninstallString")}, true, nil
	}
	return UninstallEntry{}, false, errors.New("chave fora das chaves Uninstall do HKLM")
}

// SplitCommandLine separa a linha de comando como o Windows (CommandLineToArgv).
func SplitCommandLine(line string) ([]string, error) { return windows.DecomposeCommandLine(line) }
