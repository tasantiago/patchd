package protocol

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

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
	Ring               int        `json:"ring"`                           // grupo de implantação (Aula 8.3): 0 = piloto, 1 = padrão
	CheckinTLS         *bool      `json:"checkin_tls,omitempty"`          // canal do último check-in (Aula 9.2); null = nenhum ainda
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
	// State: rede e discos agora (Aula 12.1). Ausente nos agentes anteriores; o servidor
	// anterior ignora o campo (o decodificador do check-in aceita campos desconhecidos).
	State *MachineState `json:"state,omitempty"`
	// Perf: as amostras de desempenho desde o último check-in aceito (docs/monitoramento.md).
	Perf []PerfSample `json:"perf,omitempty"`
	// Reason: por que o check-in veio antes da hora ("usuario", "rede", "software",
	// "desempenho", "autoatendimento"); vazio: o de hora em hora. Só para o log.
	Reason string `json:"reason,omitempty"`
	// Unblocked ("tentar assim que fechar"): jobs adiados por programa aberto cujo programa o
	// agente viu fechar. A campanha pede de novo na hora. No máximo MaxUnblocked.
	Unblocked []int64 `json:"unblocked,omitempty"`
}

// MaxUnblocked: quantos jobs liberados um check-in leva.
const MaxUnblocked = 50

// CapabilityCloseApps: o agente entende o "fechar o programa com aviso" do job de terceiros
// (o campo close). Os anteriores recusam o job com o campo: o servidor só o manda a quem anuncia.
const CapabilityCloseApps = "fechar-programa-v1"

// CapabilityJobs: o agente recebe, confere e executa jobs assinados (Aula 8.1).
const CapabilityJobs = "jobs-v1"

// RelayInfo: o endereço do relé (host:porta). O agente que não alcança o servidor direto
// (fora da rede) conecta nele; o TLS continua conferindo o nome do servidor.
type RelayInfo struct {
	Addr string `json:"addr"`
}

// CheckRelayAddr confere o endereço do relé: host (IP ou nome) e porta, sem esquema nem caminho.
func CheckRelayAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("use host:porta (ex.: 203.0.113.10:8443): %w", err)
	}
	n, err := strconv.Atoi(port)
	switch {
	case err != nil || n < 1 || n > 65535:
		return errors.New("porta de 1 a 65535")
	case host == "" || len(host) > 253:
		return errors.New("falta o host")
	case strings.ContainsAny(host, "/@?#% "):
		return errors.New("só o host e a porta, sem https:// nem caminho")
	}
	return nil
}

// ChannelPath: o canal em tempo real (long-poll). O agente faz POST e o servidor segura a
// resposta até ~45 s, ou responde antes com Wake quando há algo para a máquina.
const ChannelPath = "/api/v1/agent/canal"

// ChannelResponse é a resposta do canal.
type ChannelResponse struct {
	// Enabled: false, o canal está desligado no servidor; o agente para até o check-in dizer
	// que voltou (CheckinResponse.Channel).
	Enabled bool `json:"enabled"`
	// Wake: há algo para a máquina; o agente faz o check-in agora.
	Wake   bool   `json:"wake"`
	Reason string `json:"reason,omitempty"`
}

// CheckinResponse diz ao agente se ele precisa enviar o inventário e, quando há uma
// versão do agente publicada diferente da dele, qual é (Aula 5.4). Quando o servidor
// distribui o catálogo offline do Windows, diz qual é o atual (Aula 6.6).
type CheckinResponse struct {
	InventoryKnown bool `json:"inventory_known"` // true: o servidor já tem esse inventário; não reenviar
	// Relay (docs/rele.md): o relé para fora da rede. Ausente (servidor antigo): o agente
	// mantém o que tinha; presente com Addr vazio: o relé foi tirado.
	Relay *RelayInfo `json:"relay,omitempty"`
	// Channel: o servidor tem o canal em tempo real ligado; o agente mantém a espera aberta
	// (ChannelPath). Ausente (servidor antigo, ou desligado): só o check-in.
	Channel        bool            `json:"channel,omitempty"`
	ServerTime     time.Time       `json:"server_time"`
	AgentUpdate    *AgentUpdate    `json:"agent_update,omitempty"`
	OfflineCatalog *OfflineCatalog `json:"offline_catalog,omitempty"`
	// RepoCache: o cache de repositórios que o agente Linux deve configurar (Aula 6.6);
	// ausente, o agente remove a configuração que tiver gravado.
	RepoCache *RepoCache `json:"repo_cache,omitempty"`
	// Jobs: os jobs pendentes desta máquina, assinados (Aula 8.1). Agentes antigos ignoram.
	Jobs []SignedJob `json:"jobs,omitempty"`
	// Monitor: os limites de desempenho da frota (docs/monitoramento.md). Ausente: os padrões.
	Monitor *MonitorPolicy `json:"monitor,omitempty"`
	// CheckinMinutes: o intervalo de check-in definido no servidor (Configuração ›
	// Parâmetros › Agentes), de MinCheckinMinutes a MaxCheckinMinutes. Ausente (0): vale o do
	// próprio agente (-checkin-interval). Agente anterior: ignora o campo.
	CheckinMinutes int `json:"checkin_minutes,omitempty"`
	// WinUpdateMicrosoft: a fonte reserva do Windows está ligada no servidor (Configuração ›
	// Parâmetros): quando a busca na fonte da política falhar, o agente busca também direto na
	// Microsoft. Agente anterior: ignora o campo.
	WinUpdateMicrosoft bool `json:"winupdate_microsoft,omitempty"`
}

// Limites do intervalo de check-in definido no servidor.
const (
	MinCheckinMinutes = 5
	MaxCheckinMinutes = 24 * 60
)

// ValidCheckinMinutes diz se o intervalo mandado pelo servidor vale.
func ValidCheckinMinutes(n int) bool { return n >= MinCheckinMinutes && n <= MaxCheckinMinutes }

// RepoCache diz onde as máquinas Linux alcançam o cache de repositórios do servidor.
type RepoCache struct {
	URL      string   `json:"url"`       // ex.: http://patchd.exemplo:8080 (sem caminho)
	AptHosts []string `json:"apt_hosts"` // origens do apt que passam pelo cache (proxy por host)
}

// PackagesPath é o caminho do download dos instaladores de terceiros (Aula 14.4): o nome
// do arquivo é o SHA-256 com a extensão (.msi ou .exe), como no cache do servidor.
const PackagesPath = "/api/v1/agent/content/pacotes/"

// BundlesPath é o caminho do download dos bundles de script (Aula 23.8): o nome do arquivo
// é o SHA-256 do ZIP mais ".zip", o mesmo que vai no job assinado.
const BundlesPath = "/api/v1/agent/content/bundles/"

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
	// Evaluating: máquinas ainda sem avaliação (o servidor está calculando); Refreshing: com
	// a avaliação anterior, enquanto a de agora é feita em segundo plano.
	Evaluating int `json:"evaluating,omitempty"`
	Refreshing int `json:"refreshing,omitempty"`
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
	// Packages: os pacotes binários instalados dessa fonte (Linux), que o job update
	// atualiza (Aula 8.4). Vazio no Windows e no macOS.
	Packages []string `json:"packages,omitempty"`
}

// ComplianceDetail é o estado de uma máquina com as pendências (Aula 7.6).
type ComplianceDetail struct {
	ComplianceStatus
	AgentVersion string        `json:"agent_version,omitempty"`
	Target       string        `json:"target,omitempty"` // a atualização que resolve todas as pendências
	Items        []PendingItem `json:"items"`
	// ThirdParty: os programas de terceiros identificados (Windows e macOS, Aula 13.3).
	ThirdParty []ThirdPartyItem `json:"third_party,omitempty"`
}

// ThirdPartyItem é um programa de terceiros identificado pelo manifesto (Aula 6.5), com a
// situação em relação à versão de referência.
type ThirdPartyItem struct {
	AppID     string `json:"app_id"`              // regra do manifesto: google-chrome
	Program   string `json:"program"`             // rótulo da regra: Google Chrome
	Name      string `json:"name"`                // como está no inventário
	State     string `json:"state"`               // DESATUALIZADO, FORA DE SUPORTE, em dia...
	Installed string `json:"installed"`           // versão instalada
	Reference string `json:"reference,omitempty"` // versão de referência ou mínima
	Origin    string `json:"origin,omitempty"`    // de onde veio a referência, ou a nota da regra
	Scope     string `json:"scope,omitempty"`     // machine ou user
}

// MachineItems são as pendências de uma máquina, para a visão da frota por item (Aula
// 13.1: Patch › Pendentes).
type MachineItems struct {
	MachineID string        `json:"machine_id"`
	Hostname  string        `json:"hostname,omitempty"`
	OS        string        `json:"os,omitempty"`
	Catalog   string        `json:"catalog,omitempty"`
	Unit      string        `json:"unit,omitempty"` // pacotes (Linux) ou CVEs (Windows, macOS)
	Items     []PendingItem `json:"items"`
	// ThirdParty: os programas de terceiros identificados (Aula 13.3).
	ThirdParty []ThirdPartyItem `json:"third_party,omitempty"`
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
	// JobResultPostponed (Aula 14.9): não deu agora (programa aberto, outro instalador
	// rodando); o servidor guarda como "adiado", e a campanha pede de novo mais tarde.
	JobResultPostponed = "adiado"
)

// JobResult é o que o agente envia ao terminar (ou recusar) um job.
type JobResult struct {
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// JobResultPath é o caminho do resultado: JobResultPath + ID do job + "/result".
const JobResultPath = "/api/v1/agent/jobs/"
