package protocol

import "time"

// PatchSchemaVersion é a versão do formato do relatório de busca de atualizações.
// Versão 2: a lista única "missing" deu lugar a "scans", um resultado por fonte.
// Campos novos e opcionais (de pacote, de reinício) não mudam a versão.
const PatchSchemaVersion = 2

// MissingUpdate é uma atualização aplicável e não instalada.
type MissingUpdate struct {
	ID               string   `json:"id"`                          // UpdateID do WUA, advisory do Fedora, pacote do Ubuntu, Label do macOS
	Revision         int      `json:"revision,omitempty"`          // revisão do UpdateID no WUA
	KBs              []string `json:"kbs,omitempty"`               // números de KB, só dígitos: "5129195"
	Title            string   `json:"title"`                       // só para exibição
	Classification   string   `json:"classification,omitempty"`    // normalizada: security, critical, update, upgrade, rollup, definition, driver, bugfix...
	ClassificationID string   `json:"classification_id,omitempty"` // identificador cru da classificação na fonte
	Severity         string   `json:"severity,omitempty"`          // severidade da fonte (MsrcSeverity, severidade do advisory)
	CVEs             []string `json:"cves,omitempty"`
	Type             string   `json:"type"`                       // software ou driver
	ReleasedAt       string   `json:"released_at,omitempty"`      // AAAA-MM-DD
	Downloaded       bool     `json:"downloaded,omitempty"`       // já baixada, aguardando instalação
	RestartRequired  bool     `json:"restart_required,omitempty"` // instalar este item VAI exigir reinício (futuro)
	// Campos de pacote (Linux) e de produto (macOS).
	Package          string `json:"package,omitempty"`           // pacote binário ou produto (macOS, Safari...)
	InstalledVersion string `json:"installed_version,omitempty"` // versão instalada, quando a fonte informa
	FixedVersion     string `json:"fixed_version,omitempty"`     // versão candidata ou corrigida
	Arch             string `json:"arch,omitempty"`
	Source           string `json:"source"` // fonte da busca que encontrou o item
}

// ScanResult é o resultado de uma busca numa fonte.
type ScanResult struct {
	// Source identifica a fonte: wua-default (servidor da política: WSUS quando configurado),
	// wua-offline (catálogo wsusscn2.cab da Microsoft), apt, dnf, softwareupdate.
	Source string `json:"source"`
	// CatalogSHA256: hash do catálogo, quando a fonte é um arquivo (wsusscn2.cab).
	CatalogSHA256 string `json:"catalog_sha256,omitempty"`
	// CatalogModifiedAt: quão novo é o catálogo que a máquina tem. No apt, o Date: assinado
	// do InRelease do pocket -security; no wsusscn2.cab, a data do arquivo (chegada à máquina).
	CatalogModifiedAt *time.Time `json:"catalog_modified_at,omitempty"`
	// CatalogCheckedAt: quando a máquina verificou o catálogo pela última vez com sucesso
	// (no apt, o último "apt update" bem-sucedido). Fontes que consultam a rede na própria
	// busca (WUA online, softwareupdate) não o preenchem: a verificação é o scanned_at.
	CatalogCheckedAt *time.Time `json:"catalog_checked_at,omitempty"`
	DurationMS       int64      `json:"duration_ms"`
	// Missing: null = a busca falhou (motivo em Error); [] = nada faltando nesta fonte.
	Missing []MissingUpdate `json:"missing"`
	Error   string          `json:"error,omitempty"`
}

// RebootSignal é o que uma fonte disse sobre reinício pendente.
type RebootSignal struct {
	Source  string `json:"source"`
	Pending bool   `json:"pending"`
	Weak    bool   `json:"weak,omitempty"`  // indício fraco: não decide sozinho
	Error   string `json:"error,omitempty"` // a fonte não pôde ser lida
}

// RebootStatus é o estado de reinício pendente: algo JÁ instalado esperando reinício (presente).
type RebootStatus struct {
	// Pending: true = algum sinal forte disse que sim; false = sinais fortes lidos e nenhum
	// disse que sim; null = nenhum sinal forte disponível (o motivo vai em Note ou nos sinais).
	Pending       *bool          `json:"pending"`
	Signals       []RebootSignal `json:"signals"`
	LastBoot      *time.Time     `json:"last_boot,omitempty"`      // UTC
	RunningKernel string         `json:"running_kernel,omitempty"` // Linux
	NewestKernel  string         `json:"newest_kernel,omitempty"`  // Linux: o mais novo em /boot
	Packages      []string       `json:"packages,omitempty"`       // Ubuntu: pacotes que pediram o reinício
	Note          string         `json:"note,omitempty"`
}

// UpdateHistoryEntry é uma operação registrada no histórico de atualizações do SO.
// Histórico é auditoria, não estado: o estado vem das buscas e do nível de patch.
type UpdateHistoryEntry struct {
	Date                time.Time `json:"date"`              // UTC
	Operation           string    `json:"operation"`         // install ou uninstall
	Result              string    `json:"result"`            // succeeded, succeeded_with_errors, failed, aborted...
	HResult             string    `json:"hresult,omitempty"` // código de erro do Windows, ex.: "0x80240022"
	ID                  string    `json:"id"`
	Revision            int       `json:"revision,omitempty"`
	KBs                 []string  `json:"kbs,omitempty"`
	Title               string    `json:"title"`                           // só para exibição
	ClientApplicationID string    `json:"client_application_id,omitempty"` // quem pediu a operação (no macOS, a origem do pacote)
	ServerSelection     string    `json:"server_selection,omitempty"`      // default, managed_server (WSUS), windows_update, others
	Source              string    `json:"source"`
}

// PatchScanReport é o relatório da busca de atualizações.
type PatchScanReport struct {
	SchemaVersion int       `json:"schema_version"`
	AgentVersion  string    `json:"agent_version"`
	ScannedAt     time.Time `json:"scanned_at"` // sempre em UTC
	// Scans: um resultado por fonte consultada, cada um com a própria lista e o próprio erro.
	Scans []ScanResult `json:"scans"`
	// Reboot: null = não coletado (motivo em Errors).
	Reboot *RebootStatus `json:"reboot"`
	// History: null = não coletado (motivo em Errors); [] = histórico vazio.
	History []UpdateHistoryEntry `json:"history"`
	// Errors lista as falhas fora das buscas (reinício, histórico), uma por entrada.
	Errors []string `json:"errors,omitempty"`
}
