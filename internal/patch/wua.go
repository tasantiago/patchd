package patch

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Valores crus lidos do WUA via COM. Ficam fora do arquivo _windows.go para que o
// mapeamento seja testável em qualquer SO.

type wuaCategory struct {
	ID   string // CategoryID (GUID)
	Name string // traduzido
	Type string // "UpdateClassification", "Product", "ProductFamily", "Company"
}

type wuaUpdate struct {
	UpdateID   string
	Revision   int
	Title      string
	KBs        []string
	CVEs       []string
	Severity   string
	Type       int // 1 = software, 2 = driver
	Categories []wuaCategory
	Released   time.Time // LastDeploymentChangeTime
	Downloaded bool
}

type wuaHistory struct {
	Date                time.Time
	Operation           int // 1 = instalação, 2 = remoção
	ResultCode          int
	HResult             int64
	UpdateID            string
	Revision            int
	Title               string
	ClientApplicationID string
	ServerSelection     int
}

// Fonte registrada nos itens vindos da busca e do histórico do WUA.
const (
	wuaSearchSource  = "WUA IUpdateSearcher.Search (servidor definido pela política: WSUS quando configurado)"
	wuaHistorySource = "WUA IUpdateSearcher.QueryHistory"
)

// classificationsByID traduz o GUID da classificação para um nome estável.
// GUIDs públicos das classificações do WSUS; desconhecidos ficam sem nome, com o GUID cru.
var classificationsByID = map[string]string{
	"0fa1201d-4330-4fa8-8ae9-b877473b6441": "security",
	"e6cf1350-c01b-414d-a61f-263d14d133b4": "critical",
	"cd5ffd1e-e932-4e3a-bf74-18bf0b1bbd83": "update",
	"28bc880e-0592-4cbf-8f95-c79b17911d5f": "rollup",
	"e0789628-ce08-4437-be74-2495b842f43b": "definition",
	"ebfc1fc5-71a4-4f7b-9aca-3b9a503104a0": "driver",
	"b54e7d24-7add-428f-8b75-90a396fa584f": "feature_pack",
	"68c5b0a3-d1a6-4553-ae49-01d3a7827828": "service_pack",
	"b4832bd8-e735-4761-8daf-37f882276dab": "tool",
	"3689bdc8-b205-4af4-8d4a-a63924c5e9d5": "upgrade",
}

// OperationResultCode do WUA.
var resultNames = map[int]string{
	0: "not_started",
	1: "in_progress",
	2: "succeeded",
	3: "succeeded_with_errors",
	4: "failed",
	5: "aborted",
}

// ServerSelection do WUA.
var serverSelectionNames = map[int]string{
	0: "default",
	1: "managed_server",
	2: "windows_update",
	3: "others",
}

// kbPattern acha o trecho "KB" + dígitos, igual em qualquer idioma do título.
var kbPattern = regexp.MustCompile(`\bKB(\d{6,8})\b`)

// toMissing converte uma atualização do WUA para o protocolo.
func toMissing(u wuaUpdate) protocol.MissingUpdate {
	m := protocol.MissingUpdate{
		ID:         u.UpdateID,
		Revision:   u.Revision,
		KBs:        u.KBs,
		Title:      strings.TrimSpace(u.Title),
		Severity:   strings.TrimSpace(u.Severity),
		CVEs:       u.CVEs,
		Type:       "software",
		Downloaded: u.Downloaded,
		Source:     wuaSearchSource,
	}
	if u.Type == 2 {
		m.Type = "driver"
	}
	if !u.Released.IsZero() {
		m.ReleasedAt = u.Released.Format("2006-01-02")
	}
	for _, c := range u.Categories {
		if c.Type == "UpdateClassification" {
			m.ClassificationID = strings.ToLower(c.ID)
			m.Classification = classificationsByID[m.ClassificationID]
			break
		}
	}
	return m
}

// toHistory converte uma entrada do histórico do WUA para o protocolo.
func toHistory(h wuaHistory) protocol.UpdateHistoryEntry {
	e := protocol.UpdateHistoryEntry{
		Date:                h.Date,
		Operation:           "install",
		Result:              nameOr(resultNames, h.ResultCode),
		ID:                  h.UpdateID,
		Revision:            h.Revision,
		KBs:                 extractKBs(h.Title),
		Title:               strings.TrimSpace(h.Title),
		ClientApplicationID: strings.TrimSpace(h.ClientApplicationID),
		ServerSelection:     nameOr(serverSelectionNames, h.ServerSelection),
		Source:              wuaHistorySource,
	}
	if h.Operation == 2 {
		e.Operation = "uninstall"
	}
	if h.HResult != 0 {
		e.HResult = fmt.Sprintf("0x%08X", uint32(h.HResult))
	}
	return e
}

// extractKBs devolve os números de KB citados no título, sem repetição e sem o prefixo.
func extractKBs(title string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range kbPattern.FindAllStringSubmatch(title, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// searchResultError traduz o ResultCode da busca: 2 é sucesso; 3 devolve a lista com
// aviso de que pode estar incompleta; os demais indicam busca não concluída.
func searchResultError(code int) error {
	switch code {
	case 2:
		return nil
	case 3:
		return fmt.Errorf("busca concluída com erros (ResultCode 3): a lista pode estar incompleta")
	default:
		return fmt.Errorf("busca não concluída (ResultCode %d: %s)", code, nameOr(resultNames, code))
	}
}

func nameOr(names map[int]string, code int) string {
	if n, ok := names[code]; ok {
		return n
	}
	return fmt.Sprintf("desconhecido_%d", code)
}
