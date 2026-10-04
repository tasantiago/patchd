package protocol

import "time"

// SubmitResponse é a resposta do servidor ao receber um relatório.
type SubmitResponse struct {
	Status     string    `json:"status"`         // stored (gravado) ou unchanged (hash igual ao último)
	Hash       string    `json:"hash,omitempty"` // hash do relatório recebido
	ServerTime time.Time `json:"server_time"`    // relógio do servidor, para o agente medir o desvio do seu
}

// ErrorResponse é o corpo de toda resposta de erro da API.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail: Code é estável (para programas); Message é para pessoas, em português.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MachineSummary resume uma máquina conhecida pelo servidor.
type MachineSummary struct {
	ID                 string     `json:"id"`
	Hostname           string     `json:"hostname,omitempty"`
	OSName             string     `json:"os_name,omitempty"`
	OSVersion          string     `json:"os_version,omitempty"`
	LastSeenAt         time.Time  `json:"last_seen_at"`                   // último relatório recebido, de qualquer tipo
	InventoryHash      string     `json:"inventory_hash,omitempty"`       // hash do inventário guardado
	InventoryChangedAt *time.Time `json:"inventory_changed_at,omitempty"` // quando o inventário mudou pela última vez
	ScanReceivedAt     *time.Time `json:"scan_received_at,omitempty"`     // última busca de atualizações recebida
	RebootPending      *bool      `json:"reboot_pending,omitempty"`       // da última busca; null = não sabe
}
