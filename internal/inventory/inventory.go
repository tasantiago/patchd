package inventory

import "context"

// OSInfo identifica o sistema operacional de uma máquina.
// Build, Version e Edition são fatos brutos; Name é um rótulo legível derivado pelo
// agente, por conveniência. O nível de patch é avaliado pelo servidor (P-01).
type OSInfo struct {
	Family   string   `json:"family"`             // GOOS: windows, linux ou darwin
	ID       string   `json:"id"`                 // identificador estável: windows, macos, ubuntu, fedora...
	Name     string   `json:"name"`               // rótulo legível, ex.: "Windows 11 Pro", "Ubuntu", "macOS"
	Edition  string   `json:"edition,omitempty"`  // edição crua, ex.: "Professional" (EditionID do Windows)
	Version  string   `json:"version"`            // ex.: "25H2", "26.04", "26.5.2"
	Build    string   `json:"build,omitempty"`    // ex.: "26200.9457" (build.UBR), "25F84"
	Codename string   `json:"codename,omitempty"` // ex.: "resolute"
	Kernel   string   `json:"kernel,omitempty"`   // versão do kernel, quando o SO a expõe separadamente
	Arch     string   `json:"arch"`               // GOARCH do agente
	Hostname string   `json:"hostname"`
	Sources  []string `json:"sources"` // de onde cada dado veio (P-03)
}

// Collector coleta o inventário da máquina local. Cada SO tem sua implementação,
// escolhida na compilação; New devolve a do SO atual.
type Collector interface {
	// OS identifica o sistema operacional.
	OS(ctx context.Context) (OSInfo, error)
}
