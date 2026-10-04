package protocol

import "time"

// IdentityEvidence são os fatos que a máquina apresenta sobre si no enrollment. Nenhum
// deles É a identidade: o servidor emite o ID. Eles servem para reconhecer a mesma
// máquina depois (formatação, credencial perdida) e para denunciar clones (Aula 4.3, parte 2).
type IdentityEvidence struct {
	InstallID    string   `json:"install_id,omitempty"`    // da instalação: MachineGuid (Windows), /etc/machine-id (Linux)
	HardwareUUID string   `json:"hardware_uuid,omitempty"` // do hardware: UUID do SMBIOS, platform_UUID (macOS); cru
	Serial       string   `json:"serial,omitempty"`        // número de série do equipamento; cru
	MACs         []string `json:"macs,omitempty"`          // interfaces físicas
	Hostname     string   `json:"hostname,omitempty"`      // só para exibição: nomes mudam e se repetem
	OSFamily     string   `json:"os_family"`
}

// EnrollRequest é o pedido de registro. O token de enrollment vai no cabeçalho
// Authorization (Bearer), nunca no corpo nem na URL.
type EnrollRequest struct {
	AgentVersion string           `json:"agent_version"`
	Evidence     IdentityEvidence `json:"evidence"`
}

// EnrollResponse entrega a identidade emitida pelo servidor. A credencial aparece
// UMA vez: o servidor guarda só o hash dela.
type EnrollResponse struct {
	MachineID  string    `json:"machine_id"`
	Credential string    `json:"credential"`
	ServerTime time.Time `json:"server_time"`
}

// IdentityLink é um alerta de identidade: a máquina MachineID, ao se registrar, apresentou
// evidências que a relacionam com RelatedMachineID. Nunca é uma fusão: o administrador decide.
type IdentityLink struct {
	ID               int64     `json:"id"`
	MachineID        string    `json:"machine_id"`         // a que acabou de se registrar
	RelatedMachineID string    `json:"related_machine_id"` // a que já existia
	Relation         string    `json:"relation"`           // reenrollment, clone, same_hardware
	CreatedAt        time.Time `json:"created_at"`
}
