package inventory

import (
	"context"
	"errors"
)

// ErrNotImplemented indica uma seção do inventário ainda não implementada neste SO.
var ErrNotImplemented = errors.New("ainda não implementado neste SO")

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

// Software é um item de software instalado. Os campos são fatos brutos da fonte;
// o servidor decide o que exibir e o que avaliar (P-01).
type Software struct {
	Name                 string `json:"name"`
	Version              string `json:"version,omitempty"` // no Linux, com época quando houver: "1:3.5.7-2.fc44"
	Publisher            string `json:"publisher,omitempty"`
	InstallDate          string `json:"install_date,omitempty"`           // AAAA-MM-DD, quando a fonte informa uma data válida
	Scope                string `json:"scope"`                            // "machine" ou "user"
	User                 string `json:"user,omitempty"`                   // SID do dono, em instalações por usuário no Windows
	Arch                 string `json:"arch,omitempty"`                   // "x64"/"x86" (visão do registro) ou a do pacote (amd64, x86_64, noarch...)
	ProductCode          string `json:"product_code,omitempty"`           // {GUID} de instalações MSI
	SourcePackage        string `json:"source_package,omitempty"`         // Linux: pacote fonte (unidade dos avisos do Ubuntu)
	SourcePackageVersion string `json:"source_package_version,omitempty"` // Linux: versão do pacote fonte
	SystemComponent      bool   `json:"system_component,omitempty"`       // oculto no Adicionar/Remover Programas
	IsUpdate             bool   `json:"is_update,omitempty"`              // entrada que representa atualização de outro produto
	Held                 bool   `json:"held,omitempty"`                   // travado no gerenciador de pacotes: não será atualizado
	Source               string `json:"source"`                           // de onde o dado veio (P-03)
}

// Hardware descreve a máquina física ou virtual. Valores crus da fonte: seriais
// genéricos ("To be filled by O.E.M.") são filtrados no servidor (Aula 4.3).
type Hardware struct {
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	SerialNumber string   `json:"serial_number,omitempty"`
	UUID         string   `json:"uuid,omitempty"`         // UUID do SMBIOS
	ChassisType  string   `json:"chassis_type,omitempty"` // código SMBIOS cru (ex.: 3 = desktop, 9 e 10 = notebook)
	BIOSVendor   string   `json:"bios_vendor,omitempty"`
	BIOSVersion  string   `json:"bios_version,omitempty"`
	BIOSDate     string   `json:"bios_date,omitempty"`
	CPUModel     string   `json:"cpu_model,omitempty"`
	CPUThreads   int      `json:"cpu_threads,omitempty"` // processadores lógicos
	MemoryBytes  uint64   `json:"memory_bytes,omitempty"`
	Sources      []string `json:"sources"` // de onde cada dado veio (P-03)
}

// Collector coleta o inventário da máquina local. Cada SO tem sua implementação,
// escolhida na compilação; New devolve a do SO atual.
type Collector interface {
	// OS identifica o sistema operacional.
	OS(ctx context.Context) (OSInfo, error)
	// Software lista o software instalado. Em falha parcial, devolve o que
	// conseguiu coletar junto com o erro. Seção não implementada: ErrNotImplemented.
	Software(ctx context.Context) ([]Software, error)
	// Hardware descreve a máquina. Em falha parcial (ex.: campo que exige root),
	// devolve os campos lidos junto com o erro. Não implementada: ErrNotImplemented.
	Hardware(ctx context.Context) (Hardware, error)
}
