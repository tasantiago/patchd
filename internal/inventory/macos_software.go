package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// spApplications é o recorte de "system_profiler SPApplicationsDataType -json" usado pelo inventário.
type spApplications struct {
	Items []spApp `json:"SPApplicationsDataType"`
}

type spApp struct {
	Name         string   `json:"_name"`
	Path         string   `json:"path"`
	Version      string   `json:"version"`
	ObtainedFrom string   `json:"obtained_from"`
	ArchKind     string   `json:"arch_kind"`
	SignedBy     []string `json:"signed_by"`
}

// archKinds traduz o arch_kind do system_profiler; valores desconhecidos seguem crus.
var archKinds = map[string]string{
	"arch_arm":     "arm64",
	"arch_i64":     "x86_64",
	"arch_arm_i64": "universal",
}

// developerIDPrefix abre o nome do certificado de apps distribuídos fora da App Store.
const developerIDPrefix = "Developer ID Application: "

// parseSPApplications converte os aplicativos do system_profiler em Software.
func parseSPApplications(data []byte) ([]Software, error) {
	var sp spApplications
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, fmt.Errorf("SPApplicationsDataType: JSON inválido: %w", err)
	}
	list := make([]Software, 0, len(sp.Items))
	for _, a := range sp.Items {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		s := Software{
			Name:            name,
			Version:         strings.TrimSpace(a.Version),
			Publisher:       publisherFromSigning(a.ObtainedFrom, a.SignedBy),
			Scope:           "machine",
			Arch:            archLabel(a.ArchKind),
			SystemComponent: strings.HasPrefix(a.Path, "/System/"),
			Source:          a.Path,
		}
		if user, ok := userFromPath(a.Path); ok {
			s.Scope = "user"
			s.User = user
		}
		list = append(list, s)
	}
	return list, nil
}

// publisherFromSigning deduz o fornecedor: "Apple" para apps da Apple; para os demais,
// o nome do certificado Developer ID sem o Team ID ("Google LLC (EQHXZ8M8AV)" vira "Google LLC").
// Apps da Mac App Store são assinados pela Apple em nome do desenvolvedor: fornecedor vazio.
func publisherFromSigning(obtainedFrom string, signedBy []string) string {
	if obtainedFrom == "apple" {
		return "Apple"
	}
	if len(signedBy) == 0 {
		return ""
	}
	leaf, ok := strings.CutPrefix(signedBy[0], developerIDPrefix)
	if !ok {
		return ""
	}
	if i := strings.LastIndex(leaf, " ("); i > 0 && strings.HasSuffix(leaf, ")") {
		leaf = leaf[:i]
	}
	return strings.TrimSpace(leaf)
}

// archLabel traduz o arch_kind; valores desconhecidos seguem crus.
func archLabel(kind string) string {
	if v, ok := archKinds[kind]; ok {
		return v
	}
	return kind
}

// userFromPath devolve o usuário dono de um app instalado dentro de /Users/<nome>/.
// A pasta compartilhada /Users/Shared não pertence a um usuário.
func userFromPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "/Users/")
	if !ok {
		return "", false
	}
	user, _, _ := strings.Cut(rest, "/")
	if user == "" || user == "Shared" {
		return "", false
	}
	return user, true
}

// collectDarwinApps lista os aplicativos (.app) pelo system_profiler.
// Fica fora do arquivo _darwin.go para ser testável em qualquer SO.
func collectDarwinApps(ctx context.Context, run platform.Runner) ([]Software, error) {
	out, err := run.Run(ctx, systemProfilerPath, "SPApplicationsDataType", "-json")
	if err != nil {
		return nil, err
	}
	return parseSPApplications(out)
}
