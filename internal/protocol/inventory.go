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
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	SerialNumber string   `json:"serial_number,omitempty"`
	UUID         string   `json:"uuid,omitempty"`         // UUID do SMBIOS ou de plataforma
	ChassisType  string   `json:"chassis_type,omitempty"` // código SMBIOS cru (ex.: 3 = desktop, 9 e 10 = notebook)
	BIOSVendor   string   `json:"bios_vendor,omitempty"`
	BIOSVersion  string   `json:"bios_version,omitempty"`
	BIOSDate     string   `json:"bios_date,omitempty"`
	CPUModel     string   `json:"cpu_model,omitempty"`
	CPUThreads   int      `json:"cpu_threads,omitempty"` // processadores lógicos
	MemoryBytes  uint64   `json:"memory_bytes,omitempty"`
	Sources      []string `json:"sources"` // de onde cada dado veio (P-03)
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

// AddErrors acrescenta cada linha de err como uma entrada própria em Errors, com o
// prefixo da seção. errors.Join junta mensagens com quebra de linha; aqui elas se separam.
func (r *InventoryReport) AddErrors(section string, err error) {
	if err == nil {
		return
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r.Errors = append(r.Errors, section+": "+line)
		}
	}
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
