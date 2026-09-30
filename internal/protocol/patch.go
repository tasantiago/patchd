package protocol

import (
	"strings"
	"time"
)

// PatchSchemaVersion é a versão do formato do relatório de busca de atualizações.
const PatchSchemaVersion = 1

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
	Source           string   `json:"source"`                // de onde a lista veio (P-03)
}

// UpdateHistoryEntry é uma operação registrada no histórico de atualizações do SO.
// Histórico é auditoria, não estado: o estado vem da busca e do nível de patch.
type UpdateHistoryEntry struct {
	Date                time.Time `json:"date"`
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
	// Missing: null = não coletado (motivo em Errors); [] = nenhuma atualização faltando.
	Missing []MissingUpdate `json:"missing"`
	// History: null = não coletado (motivo em Errors); [] = histórico vazio.
	History []UpdateHistoryEntry `json:"history"`
	// Errors lista as falhas, uma por entrada, com o prefixo da seção.
	Errors []string `json:"errors,omitempty"`
}

// AddErrors acrescenta cada linha de err como uma entrada própria em Errors, com o prefixo da seção.
func (r *PatchScanReport) AddErrors(section string, err error) {
	if err == nil {
		return
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r.Errors = append(r.Errors, section+": "+line)
		}
	}
}
