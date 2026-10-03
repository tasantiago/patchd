package patch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Fonte e caminhos absolutos do macOS (o Runner recusa nomes relativos).
const (
	SourceSoftwareUpdate    = "softwareupdate"
	softwareUpdatePath      = "/usr/sbin/softwareupdate"
	swVersPath              = "/usr/bin/sw_vers"
	darwinProfilerPath      = "/usr/sbin/system_profiler"
	darwinHistorySource     = "system_profiler SPInstallHistoryDataType"
	softwareUpdateItemStart = "* Label:"
)

// parseSoftwareUpdateList lê a saída do "softwareupdate --list". Cada item tem duas linhas:
//
//   - Label: macOS Tahoe 26.7.1-25G241
//     Title: macOS Tahoe 26.7.1, Version: 26.7.1, Size: 3852999KiB, Recommended: YES, Action: restart,
//
// currentOS é a versão do macOS da máquina (sw_vers -productVersion): um item do macOS com
// versão principal maior que a atual é troca de versão ("upgrade"); os demais são "update".
func parseSoftwareUpdateList(data []byte, currentOS string) []protocol.MissingUpdate {
	var list []protocol.MissingUpdate
	var cur *protocol.MissingUpdate
	currentMajor := majorOf(currentOS)

	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if label, ok := strings.CutPrefix(t, softwareUpdateItemStart); ok {
			flush()
			cur = &protocol.MissingUpdate{
				ID:             strings.TrimSpace(label),
				Type:           "software",
				Classification: "update",
				Source:         SourceSoftwareUpdate,
			}
			continue
		}
		if cur == nil || !strings.HasPrefix(t, "Title:") {
			continue
		}
		f := parseSoftwareUpdateFields(t)
		cur.Title = f["Title"]
		cur.FixedVersion = f["Version"]
		cur.RestartRequired = strings.EqualFold(f["Action"], "restart")
		cur.Package = cur.Title
		if strings.HasPrefix(cur.Title, "macOS") {
			cur.Package = "macOS"
			if m := majorOf(cur.FixedVersion); m > 0 && currentMajor > 0 && m > currentMajor {
				cur.Classification = "upgrade"
			}
		}
	}
	flush()
	return list
}

// parseSoftwareUpdateFields lê "Chave: valor, Chave: valor,". Um pedaço sem ": " ou com
// espaço na chave é continuação do valor anterior (um título com vírgula, por exemplo).
// Chaves desconhecidas são lidas e ignoradas por quem chama.
func parseSoftwareUpdateFields(line string) map[string]string {
	out := map[string]string{}
	last := ""
	for _, part := range strings.Split(strings.TrimSuffix(strings.TrimSpace(line), ","), ", ") {
		k, v, ok := strings.Cut(part, ": ")
		if ok && k != "" && !strings.Contains(k, " ") {
			out[k] = strings.TrimSpace(v)
			last = k
			continue
		}
		if last != "" {
			out[last] += ", " + part
		}
	}
	return out
}

// majorOf devolve a versão principal ("26" em "26.7.1"); 0 se não for numérica.
func majorOf(version string) int {
	head, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
}

// spInstallHistory é o recorte de "system_profiler SPInstallHistoryDataType -json".
type spInstallHistory struct {
	Items []struct {
		Name           string `json:"_name"`
		InstallDate    string `json:"install_date"`
		InstallVersion string `json:"install_version"`
		PackageSource  string `json:"package_source"`
	} `json:"SPInstallHistoryDataType"`
}

// parseInstallHistory converte o histórico de instalações do macOS, da mais recente
// para a mais antiga, limitado a max. O macOS só registra instalações concluídas.
func parseInstallHistory(data []byte, max int) ([]protocol.UpdateHistoryEntry, error) {
	var sp spInstallHistory
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, fmt.Errorf("SPInstallHistoryDataType: JSON inválido: %w", err)
	}

	list := make([]protocol.UpdateHistoryEntry, 0, len(sp.Items))
	bad := 0
	for _, it := range sp.Items {
		t, err := time.Parse(time.RFC3339, it.InstallDate)
		if err != nil {
			bad++
			continue
		}
		title := strings.TrimSpace(it.Name)
		if v := strings.TrimSpace(it.InstallVersion); v != "" && !strings.Contains(title, v) {
			title += " " + v
		}
		list = append(list, protocol.UpdateHistoryEntry{
			Date:                t.UTC(),
			Operation:           "install",
			Result:              "succeeded",
			ID:                  strings.TrimSpace(it.Name),
			Title:               title,
			ClientApplicationID: it.PackageSource,
			Source:              darwinHistorySource,
		})
	}

	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })
	if len(list) > max {
		list = list[:max]
	}
	if bad > 0 {
		return list, fmt.Errorf("SPInstallHistoryDataType: %d entrada(s) com data ilegível", bad)
	}
	return list, nil
}
