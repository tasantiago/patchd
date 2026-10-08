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
	AgentVersion       string     `json:"agent_version,omitempty"`        // do último check-in (ou do registro)
	LastSeenAt         time.Time  `json:"last_seen_at"`                   // último relatório recebido, de qualquer tipo
	InventoryHash      string     `json:"inventory_hash,omitempty"`       // hash do inventário guardado
	InventoryChangedAt *time.Time `json:"inventory_changed_at,omitempty"` // quando o inventário mudou pela última vez
	ScanReceivedAt     *time.Time `json:"scan_received_at,omitempty"`     // última busca de atualizações recebida
	RebootPending      *bool      `json:"reboot_pending,omitempty"`       // da última busca; null = não sabe
}

// CheckinRequest é o contato periódico do agente: barato, sem o inventário. O servidor
// atualiza o último contato e diz se já tem o inventário com esse hash.
type CheckinRequest struct {
	AgentVersion  string    `json:"agent_version"`
	InventoryHash string    `json:"inventory_hash"` // hash do inventário que o agente acabou de coletar
	SentAt        time.Time `json:"sent_at"`        // relógio do agente, para o desvio
}

// CheckinResponse diz ao agente se ele precisa enviar o inventário e, quando há uma
// versão do agente publicada diferente da dele, qual é (Aula 5.4). Quando o servidor
// distribui o catálogo offline do Windows, diz qual é o atual (Aula 6.6).
type CheckinResponse struct {
	InventoryKnown bool            `json:"inventory_known"` // true: o servidor já tem esse inventário; não reenviar
	ServerTime     time.Time       `json:"server_time"`
	AgentUpdate    *AgentUpdate    `json:"agent_update,omitempty"`
	OfflineCatalog *OfflineCatalog `json:"offline_catalog,omitempty"`
	// RepoCache: o cache de repositórios que o agente Linux deve configurar (Aula 6.6);
	// ausente, o agente remove a configuração que tiver gravado.
	RepoCache *RepoCache `json:"repo_cache,omitempty"`
}

// RepoCache diz onde as máquinas Linux alcançam o cache de repositórios do servidor.
type RepoCache struct {
	URL      string   `json:"url"`       // ex.: http://patchd.exemplo:8080 (sem caminho)
	AptHosts []string `json:"apt_hosts"` // origens do apt que passam pelo cache (proxy por host)
}

// OfflineCatalogPath é o caminho do download do catálogo offline; o SHA-256 vem no fim.
const OfflineCatalogPath = "/api/v1/agent/content/wsusscn2/"

// OfflineCatalog é o wsusscn2.cab que o servidor distribui. O agente do Windows baixa de
// OfflineCatalogPath + SHA256, confere o tamanho, o SHA-256 e a assinatura da Microsoft,
// e só então passa a usá-lo na busca offline. Os outros agentes ignoram.
type OfflineCatalog struct {
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	PublishedAt time.Time `json:"published_at"` // publicação pela Microsoft (Last-Modified)
}

// AgentUpdate é a oferta de atualização. É só uma sugestão: o agente baixa o manifesto,
// confere a assinatura com a chave embutida nele e recusa qualquer versão que não seja
// mais nova que a sua.
type AgentUpdate struct {
	Version string `json:"version"`
}
