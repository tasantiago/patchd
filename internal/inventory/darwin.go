package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// Caminhos absolutos no macOS (o Runner recusa nomes relativos).
const (
	plutilPath         = "/usr/bin/plutil"
	unamePath          = "/usr/bin/uname"
	systemVersionPlist = "/System/Library/CoreServices/SystemVersion.plist"
)

// systemVersion são os campos do SystemVersion.plist usados pelo inventário.
type systemVersion struct {
	ProductName         string `json:"ProductName"`
	ProductVersion      string `json:"ProductVersion"`
	ProductBuildVersion string `json:"ProductBuildVersion"`
}

// parseSystemVersion interpreta o SystemVersion.plist já convertido para JSON pelo plutil.
func parseSystemVersion(data []byte) (systemVersion, error) {
	var sv systemVersion
	if err := json.Unmarshal(data, &sv); err != nil {
		return sv, fmt.Errorf("SystemVersion.plist: JSON inválido: %w", err)
	}
	if sv.ProductVersion == "" || sv.ProductBuildVersion == "" {
		return sv, errors.New("SystemVersion.plist: faltam ProductVersion ou ProductBuildVersion")
	}
	return sv, nil
}

// collectDarwinOS monta o OSInfo do macOS. Fica fora do arquivo _darwin.go para
// poder ser testada em qualquer SO com um Runner falso.
func collectDarwinOS(ctx context.Context, run platform.Runner, hostname func() (string, error)) (OSInfo, error) {
	info := OSInfo{Family: "darwin", ID: "macos", Arch: runtime.GOARCH}

	out, err := run.Run(ctx, plutilPath, "-convert", "json", "-o", "-", systemVersionPlist)
	if err != nil {
		return info, err
	}
	sv, err := parseSystemVersion(out)
	if err != nil {
		return info, err
	}
	info.Name = sv.ProductName
	info.Version = sv.ProductVersion
	info.Build = sv.ProductBuildVersion
	info.Sources = append(info.Sources, systemVersionPlist)

	// Kernel (Darwin) é informativo: se falhar, o inventário segue sem ele.
	if k, err := run.Run(ctx, unamePath, "-r"); err == nil {
		info.Kernel = strings.TrimSpace(string(k))
		info.Sources = append(info.Sources, unamePath+" -r")
	}
	if h, err := hostname(); err == nil {
		info.Hostname = h
	}
	return info, nil
}
