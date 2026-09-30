package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// cryptexAppsDir guarda os aplicativos que a Apple atualiza separadamente do macOS
// (o Safari, por exemplo). /Applications/Safari.app é um link simbólico para cá,
// e o system_profiler não lista esses aplicativos.
const cryptexAppsDir = "/System/Cryptexes/App/System/Applications"

// collectCryptexApps lê os .app do cryptex de aplicativos pelo Info.plist de cada um.
// Pasta inexistente (versões do macOS sem cryptex) não é erro. Fica fora do arquivo
// _darwin.go para ser testável em qualquer SO.
func collectCryptexApps(ctx context.Context, run platform.Runner, listDir func(string) ([]string, error)) ([]Software, error) {
	names, err := listDir(cryptexAppsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cryptexAppsDir, err)
	}

	var list []Software
	var errs []error
	for _, n := range names {
		if !strings.HasSuffix(n, ".app") {
			continue
		}
		appPath := cryptexAppsDir + "/" + n
		// O Info.plist costuma ser binário: o plutil converte para XML, lido pelo parsePlistDict.
		out, err := run.Run(ctx, plutilPath, "-convert", "xml1", "-o", "-", appPath+"/Contents/Info.plist")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", appPath, err))
			continue
		}
		d, err := parsePlistDict(out)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", appPath, err))
			continue
		}
		name := strings.TrimSpace(d["CFBundleName"])
		if name == "" {
			name = strings.TrimSuffix(n, ".app")
		}
		list = append(list, Software{
			Name:      name,
			Version:   strings.TrimSpace(d["CFBundleShortVersionString"]),
			Publisher: "Apple",
			Scope:     "machine",
			// Visível: tem atualização própria, separada do macOS.
			SystemComponent: false,
			Source:          appPath,
		})
	}
	return list, errors.Join(errs...)
}

// listDirNames devolve os nomes das entradas de uma pasta (os.ReadDir, sem os detalhes).
func listDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
