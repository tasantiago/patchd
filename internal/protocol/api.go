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
	Ring               int        `json:"ring"`                           // anel (Aula 8.3): 0 = piloto, 1 = padrão
}

// CheckinRequest é o contato periódico do agente: barato, sem o inventário. O servidor
// atualiza o último contato e diz se já tem o inventário com esse hash.
type CheckinRequest struct {
	AgentVersion  string    `json:"agent_version"`
	InventoryHash string    `json:"inventory_hash"` // hash do inventário que o agente acabou de coletar
	SentAt        time.Time `json:"sent_at"`        // relógio do agente, para o desvio
	// Capabilities: o que este agente sabe fazer além do básico (Aula 8.1). O servidor só
	// entrega jobs a quem anuncia CapabilityJobs: um agente antigo os ignoraria em silêncio.
	Capabilities []string `json:"capabilities,omitempty"`
}

// CapabilityJobs: o agente recebe, confere e executa jobs assinados (Aula 8.1).
const CapabilityJobs = "jobs-v1"

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
	// Jobs: os jobs pendentes desta máquina, assinados (Aula 8.1). Agentes antigos ignoram.
	Jobs []SignedJob `json:"jobs,omitempty"`
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

// ComplianceStatus é o estado de compliance de uma máquina (RF-23, Aula 7.4).
type ComplianceStatus struct {
	MachineID string `json:"machine_id"`
	Hostname  string `json:"hostname,omitempty"`
	OS        string `json:"os,omitempty"` // nome e versão, como o inventário informa
	// State: em_dia, faltando, reboot_pendente, sem_dado_recente ou desconhecido.
	State       string    `json:"state"`
	Reasons     []string  `json:"reasons"`
	Pending     int       `json:"pending"`                // com correção pendente
	PendingUnit string    `json:"pending_unit,omitempty"` // pacotes (Linux) ou CVEs (Windows, macOS)
	Exploited   int       `json:"exploited"`              // dos pendentes, com exploração conhecida ou inferida
	Catalog     string    `json:"catalog,omitempty"`      // contra o que foi avaliada (ecossistema, produto, major)
	LastSeenAt  time.Time `json:"last_seen_at"`
	// InventoryCollectedAt: quando o inventário avaliado foi coletado na máquina.
	InventoryCollectedAt *time.Time `json:"inventory_collected_at,omitempty"`
	// CatalogWarnings: fontes do catálogo atrasadas; a avaliação pode estar perdendo correções.
	CatalogWarnings []string  `json:"catalog_warnings,omitempty"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

// ComplianceFleet é o estado da frota inteira.
type ComplianceFleet struct {
	EvaluatedAt time.Time          `json:"evaluated_at"`
	Counts      map[string]int     `json:"counts"` // por estado, com os cinco sempre presentes
	Machines    []ComplianceStatus `json:"machines"`
}

// PendingItem é uma pendência da máquina: um pacote fonte (Linux) ou uma CVE (Windows,
// macOS) abaixo da correção (Aula 7.6).
type PendingItem struct {
	ID        string `json:"id"`                 // nome do pacote fonte ou CVE
	Severity  string `json:"severity,omitempty"` // como a fonte informa
	Exploited bool   `json:"exploited"`          // KEV, MSRC ou exploração inferida
	Installed string `json:"installed,omitempty"`
	FixedIn   string `json:"fixed_in,omitempty"` // versão, build ou KB que corrige
	Note      string `json:"note,omitempty"`     // avisos (USN, FEDORA-...) ou título da CVE
}

// ComplianceDetail é o estado de uma máquina com as pendências (Aula 7.6).
type ComplianceDetail struct {
	ComplianceStatus
	AgentVersion string        `json:"agent_version,omitempty"`
	Target       string        `json:"target,omitempty"` // a atualização que resolve todas as pendências
	Items        []PendingItem `json:"items"`
}

// RetiredMachine é uma máquina aposentada (Aula 7.5): fora da frota, quando, por quê e por quem.
type RetiredMachine struct {
	MachineSummary
	RetiredAt time.Time `json:"retired_at"`
	Reason    string    `json:"reason"`
	RetiredBy string    `json:"retired_by,omitempty"` // usuário do painel ou "linha de comando"
}

// SignedJob é um job assinado pelo servidor (Aula 8.1). Payload é o JSON do job, como
// texto: os bytes exatos que foram assinados.
type SignedJob struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"` // Ed25519, base64
}

// Resultados que o agente informa para um job.
const (
	JobResultOK       = "ok"        // executou; o servidor ainda confere pelo efeito (RF-08)
	JobResultFailed   = "falhou"    // tentou e não conseguiu
	JobResultRejected = "rejeitado" // não executou: assinatura, máquina, prazo, tipo
)

// JobResult é o que o agente envia ao terminar (ou recusar) um job.
type JobResult struct {
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// JobResultPath é o caminho do resultado: JobResultPath + ID do job + "/result".
const JobResultPath = "/api/v1/agent/jobs/"
