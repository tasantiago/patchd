package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// InventorySchemaVersion é a versão do formato do relatório de inventário.
// Campo novo e opcional não muda a versão (quem lê ignora o que não conhece);
// mudar o significado de um campo, ou removê-lo, muda.
const InventorySchemaVersion = 1

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
	User                 string `json:"user,omitempty"`                   // SID (Windows) ou nome (macOS) do dono, em instalações por usuário
	Arch                 string `json:"arch,omitempty"`                   // "x64"/"x86" (visão do registro) ou a do pacote/app
	ProductCode          string `json:"product_code,omitempty"`           // {GUID} de instalações MSI
	BundleUpgradeCode    string `json:"bundle_upgrade_code,omitempty"`    // {GUID} de bundles do WiX Burn (Aula 14.3)
	SourcePackage        string `json:"source_package,omitempty"`         // Linux: pacote fonte (unidade dos avisos do Ubuntu)
	SourcePackageVersion string `json:"source_package_version,omitempty"` // Linux: versão do pacote fonte
	SystemComponent      bool   `json:"system_component,omitempty"`       // parte do SO, oculto nas listas de programas
	IsUpdate             bool   `json:"is_update,omitempty"`              // entrada que representa atualização de outro produto
	Held                 bool   `json:"held,omitempty"`                   // travado no gerenciador de pacotes: não será atualizado
	Source               string `json:"source"`                           // de onde o dado veio (P-03)
}

// Hardware descreve a máquina física ou virtual. Valores crus da fonte: seriais
// genéricos ("To be filled by O.E.M.") são filtrados no servidor (Aula 4.3).
type Hardware struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	UUID         string `json:"uuid,omitempty"`         // UUID do SMBIOS ou de plataforma
	ChassisType  string `json:"chassis_type,omitempty"` // código SMBIOS cru (ex.: 3 = desktop, 9 e 10 = notebook)
	BIOSVendor   string `json:"bios_vendor,omitempty"`
	BIOSVersion  string `json:"bios_version,omitempty"`
	BIOSDate     string `json:"bios_date,omitempty"`
	CPUModel     string `json:"cpu_model,omitempty"`
	CPUThreads   int    `json:"cpu_threads,omitempty"` // processadores lógicos
	MemoryBytes  uint64 `json:"memory_bytes,omitempty"`
	// Firmware (Aula 21.1): o estado do firmware, só leitura. nil: agente anterior.
	Firmware *Firmware `json:"firmware,omitempty"`
	// Wake (Aula 22.1): ligar pela rede, só leitura. nil: agente anterior ou sistema sem a coleta.
	Wake    *Wake    `json:"wake,omitempty"`
	Sources []string `json:"sources"` // de onde cada dado veio (P-03)
}

// Firmware é o estado do firmware (BIOS/UEFI) como a máquina informa, só leitura (Aula 21.1).
// Ponteiro nil e texto vazio: não se sabe (sem a interface, sem permissão, sistema sem a
// informação). Senha: só se está definida, nunca nada da senha.
type Firmware struct {
	Mode       string `json:"mode,omitempty"`        // FirmwareUEFI ou FirmwareLegacy
	SecureBoot *bool  `json:"secure_boot,omitempty"` // Secure Boot ligado
	TPM        string `json:"tpm,omitempty"`         // "2.0", "1.2" ou TPMNone
	// Interface: a interface do fabricante que respondeu (FirmwareHP, FirmwareDell,
	// FirmwareLenovo); vazio: nenhuma, e as senhas ficam sem informação.
	Interface       string `json:"interface,omitempty"`
	AdminPassword   *bool  `json:"admin_password,omitempty"`    // senha de administrador (setup/supervisor) definida
	PowerOnPassword *bool  `json:"power_on_password,omitempty"` // senha de ligar definida
	// SureAdmin (Aula 21.5, só HP): o modo de autenticação reforçado da BIOS (HP Sure Admin,
	// "Enhanced BIOS Authentication Mode") está ligado. Nesse modo a BIOS recusa a troca de
	// senha por senha (exige um pacote assinado), e o patchd não grava. nil: não se sabe.
	SureAdmin *bool `json:"sure_admin,omitempty"`
	// Settings (docs/bios-config.md: HP, Dell e Lenovo): as configurações da BIOS como a interface do
	// fabricante informa, sem as senhas. nil: não lidas.
	Settings []BIOSSetting `json:"settings,omitempty"`
}

// BIOSSetting é uma configuração da BIOS. Valores crus da interface do fabricante: o que pode
// ser alterado pelo patchd é decidido pelo servidor e pelo agente (internal/biosconfig).
type BIOSSetting struct {
	Name     string   `json:"name"`
	Path     string   `json:"path,omitempty"` // o caminho no menu da BIOS, ex.: \Advanced\Boot Options
	Kind     string   `json:"kind"`           // BIOSKindEnum, BIOSKindString, BIOSKindInteger ou BIOSKindList
	Value    string   `json:"value"`
	Possible []string `json:"possible,omitempty"` // os valores aceitos (só BIOSKindEnum)
	ReadOnly bool     `json:"read_only,omitempty"`
}

// Tipos de configuração da BIOS.
const (
	BIOSKindEnum    = "escolha"
	BIOSKindString  = "texto"
	BIOSKindInteger = "numero"
	BIOSKindList    = "lista"
)

// Limites das configurações da BIOS: uma BIOS estranha não infla o relatório.
const (
	MaxBIOSSettings = 600
	MaxBIOSPossible = 64
	MaxBIOSName     = 120
	MaxBIOSValue    = 256
)

// checkBIOSSettings confere tamanhos, tipos e nomes repetidos.
func checkBIOSSettings(list []BIOSSetting) error {
	if len(list) > MaxBIOSSettings {
		return fmt.Errorf("configurações da BIOS demais: %d", len(list))
	}
	bad := func(v string, max int) bool {
		return len(v) > max || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f })
	}
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		switch {
		case s.Name == "" || bad(s.Name, MaxBIOSName):
			return fmt.Errorf("configuração da BIOS com nome vazio, longo demais ou com caractere de controle")
		case seen[s.Name]:
			return fmt.Errorf("configuração da BIOS repetida %q", s.Name)
		case bad(s.Path, MaxBIOSValue) || bad(s.Value, MaxBIOSValue):
			return fmt.Errorf("configuração da BIOS %q: caminho ou valor longo demais ou com caractere de controle", s.Name)
		case len(s.Possible) > MaxBIOSPossible:
			return fmt.Errorf("configuração da BIOS %q: valores possíveis demais", s.Name)
		}
		seen[s.Name] = true
		switch s.Kind {
		case BIOSKindEnum:
		case BIOSKindString, BIOSKindInteger, BIOSKindList:
			if len(s.Possible) > 0 {
				return fmt.Errorf("configuração da BIOS %q: valores possíveis só valem para escolha", s.Name)
			}
		default:
			return fmt.Errorf("configuração da BIOS %q: tipo desconhecido %q", s.Name, s.Kind)
		}
		for _, v := range s.Possible {
			if v == "" || bad(v, MaxBIOSValue) {
				return fmt.Errorf("configuração da BIOS %q: valor possível vazio ou inválido", s.Name)
			}
		}
	}
	return nil
}

// Empty diz se nada do firmware foi informado.
func (f Firmware) Empty() bool {
	return f.Mode == "" && f.SecureBoot == nil && f.TPM == "" && f.Interface == "" && f.AdminPassword == nil &&
		f.PowerOnPassword == nil && f.SureAdmin == nil && f.Settings == nil
}

// Valores do Firmware.
const (
	FirmwareUEFI   = "uefi"
	FirmwareLegacy = "legado"
	TPMNone        = "nenhum"
	FirmwareHP     = "hp"
	FirmwareDell   = "dell"
	FirmwareLenovo = "lenovo"
)

// Check confere os valores fechados: o painel mostra o que passou daqui.
func (f Firmware) Check() error {
	switch f.Mode {
	case "", FirmwareUEFI, FirmwareLegacy:
	default:
		return fmt.Errorf("modo do firmware desconhecido %q", f.Mode)
	}
	switch f.TPM {
	case "", "1.2", "2.0", TPMNone:
	default:
		return fmt.Errorf("versão de TPM desconhecida %q", f.TPM)
	}
	switch f.Interface {
	case "", FirmwareHP, FirmwareDell, FirmwareLenovo:
	default:
		return fmt.Errorf("interface do fabricante desconhecida %q", f.Interface)
	}
	if f.Interface == "" && (f.AdminPassword != nil || f.PowerOnPassword != nil) {
		return fmt.Errorf("senha informada sem a interface do fabricante")
	}
	if f.SureAdmin != nil && f.Interface != FirmwareHP {
		return fmt.Errorf("sure_admin é só da interface da HP")
	}
	if f.Settings != nil && f.Interface == "" {
		return fmt.Errorf("configurações da BIOS sem a interface do fabricante")
	}
	return checkBIOSSettings(f.Settings)
}

// InventoryReport é o relatório de inventário enviado pelo agente ao servidor.
type InventoryReport struct {
	SchemaVersion int       `json:"schema_version"`
	AgentVersion  string    `json:"agent_version"`
	CollectedAt   time.Time `json:"collected_at"` // sempre em UTC
	// Hash do conteúdo (ver ContentHash): igual entre coletas sem mudança.
	Hash string `json:"hash"`
	OS   OSInfo `json:"os"`
	// Hardware: null = não coletado (motivo em Errors); objeto = coletado, talvez parcialmente.
	Hardware *Hardware `json:"hardware"`
	// Software: null = não coletado (motivo em Errors); [] = coletado, nenhum item.
	Software []Software `json:"software"`
	// Errors lista as falhas, uma por entrada, com o prefixo da seção.
	Errors []string `json:"errors,omitempty"`
}

// hashInput é o que o hash cobre: só o conteúdo coletado. Ficam de fora os campos que
// mudam a cada coleta (collected_at, agent_version) e os erros.
type hashInput struct {
	SchemaVersion int        `json:"schema_version"`
	OS            OSInfo     `json:"os"`
	Hardware      *Hardware  `json:"hardware"`
	Software      []Software `json:"software"`
}

// ContentHash calcula o hash canônico do conteúdo: os mesmos dados produzem sempre os
// mesmos bytes. A lista de software é ordenada numa cópia, sem depender de o coletor
// ter ordenado. null e [] continuam distintos: "não coletado" não é "nenhum item".
func (r InventoryReport) ContentHash() (string, error) {
	software := slices.Clone(r.Software)
	SortSoftware(software)

	data, err := json.Marshal(hashInput{
		SchemaVersion: r.SchemaVersion,
		OS:            r.OS,
		Hardware:      r.Hardware,
		Software:      software,
	})
	if err != nil {
		return "", fmt.Errorf("hash do inventário: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SortSoftware ordena por nome (sem diferenciar maiúsculas), versão e fonte.
// A fonte desempata itens de mesmo nome e versão, tornando a ordem total e estável.
func SortSoftware(list []Software) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name); na != nb {
			return na < nb
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Source < b.Source
	})
}
