package protocol

import "time"

// PatchSchemaVersion é a versão do formato do relatório de busca de atualizações.
// Versão 2: a lista única "missing" deu lugar a "scans", um resultado por fonte.
const PatchSchemaVersion = 2

// MissingUpdate é uma atualização aplicável e não instalada.
type MissingUpdate struct {
	ID               string   `json:"id"`                          // identificador na fonte (UpdateID do WUA, nome do advisory...)
	Revision         int      `json:"revision,omitempty"`          // revisão do UpdateID no WUA
	KBs              []string `json:"kbs,omitempty"`               // números de KB, só dígitos: "5129195"
	Title            string   `json:"title"`                       // traduzido: só para exibição
	Classification   string   `json:"classification,omitempty"`    // normalizada: security, critical, update, rollup, definition, driver...
	ClassificationID string   `json:"classification_id,omitempty"` // identificador cru da classificação na fonte
	Severity         string   `json:"severity,omitempty"`          // severidade da fonte (MsrcSeverity: Critical, Important...)
	CVEs             []string `json:"cves,omitempty"`
	Type             string   `json:"type"`                  // software ou driver
	ReleasedAt       string   `json:"released_at,omitempty"` // AAAA-MM-DD
	Downloaded       bool     `json:"downloaded,omitempty"`  // já baixada, aguardando instalação
	Source           string   `json:"source"`                // fonte da busca que encontrou o item
}

// ScanResult é o resultado de uma busca numa fonte.
type ScanResult struct {
	// Source identifica a fonte: wua-default (servidor da política: WSUS quando configurado),
	// wua-offline (catálogo wsusscn2.cab da Microsoft); no Linux e no macOS, as fontes da 3.4 e 3.5.
	Source string `json:"source"`
	// Catálogo usado, quando a fonte é um arquivo (wsusscn2.cab): hash e data do arquivo.
	CatalogSHA256     string     `json:"catalog_sha256,omitempty"`
	CatalogModifiedAt *time.Time `json:"catalog_modified_at,omitempty"`
	DurationMS        int64      `json:"duration_ms"`
	// Missing: null = a busca falhou (motivo em Error); [] = nada faltando nesta fonte.
	Missing []MissingUpdate `json:"missing"`
	Error   string          `json:"error,omitempty"`
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
	Title               string    `json:"title"`                           // traduzido: só para exibição
	ClientApplicationID string    `json:"client_application_id,omitempty"` // quem pediu a operação
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
	// History: null = não coletado (motivo em Errors); [] = histórico vazio.
	History []UpdateHistoryEntry `json:"history"`
	// Errors lista as falhas fora das buscas (ex.: histórico), uma por entrada.
	Errors []string `json:"errors,omitempty"`
}
