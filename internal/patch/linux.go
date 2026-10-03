package patch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Caminhos absolutos no Linux (o Runner recusa nomes relativos).
const (
	aptGetPath  = "/usr/bin/apt-get"
	dnfPath     = "/usr/bin/dnf"
	aptListsDir = "/var/lib/apt/lists"
)

// aptInst lê uma linha "Inst" da simulação do apt-get:
//
//	Inst libcares2 [1.34.6-1] (1.34.6-1ubuntu0.1 Ubuntu:26.04/resolute-security [amd64])
//
// Grupos: pacote, versão instalada (ausente em pacote novo), candidata, origens, arquitetura.
var aptInst = regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]*)\] )?\((\S+) (.+?) \[([^\]]+)\]\)`)

// parseAptSimulation lê as linhas "Inst" do apt-get -s. É de segurança o item que tem
// algum pocket "-security" entre as origens. Linhas "Inst" ilegíveis viram erro parcial.
func parseAptSimulation(data []byte) ([]protocol.MissingUpdate, error) {
	var list []protocol.MissingUpdate
	var bad []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		m := aptInst.FindStringSubmatch(line)
		if m == nil {
			bad = append(bad, line)
			continue
		}
		u := protocol.MissingUpdate{
			ID:               m[1],
			Title:            m[1],
			Package:          m[1],
			InstalledVersion: m[2],
			FixedVersion:     m[3],
			Arch:             m[5],
			Type:             "software",
			Classification:   "update",
			Source:           SourceApt,
		}
		for _, origin := range strings.Split(m[4], ",") {
			origin = strings.TrimSpace(origin)
			suite := origin[strings.LastIndex(origin, "/")+1:]
			if strings.HasSuffix(suite, "-security") {
				u.Classification = "security"
			}
		}
		list = append(list, u)
	}
	if len(bad) > 0 {
		return list, fmt.Errorf("apt-get: %d linha(s) Inst fora do formato (ex.: %q)", len(bad), bad[0])
	}
	return list, nil
}

// dnfAdvisory é uma linha de "dnf advisory list --json" (um par advisory × pacote).
type dnfAdvisory struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Severity  string `json:"severity"`
	Nevra     string `json:"nevra"`
	Buildtime int64  `json:"buildtime"`
}

// parseDnfAdvisories converte os advisories do dnf em atualizações faltantes.
// Saída vazia significa nenhum advisory pendente.
func parseDnfAdvisories(data []byte) ([]protocol.MissingUpdate, error) {
	if strings.TrimSpace(string(data)) == "" {
		return []protocol.MissingUpdate{}, nil
	}
	var rows []dnfAdvisory
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("dnf advisory: JSON inválido: %w", err)
	}

	list := make([]protocol.MissingUpdate, 0, len(rows))
	var bad []string
	for _, r := range rows {
		name, evr, arch, ok := parseNEVRA(r.Nevra)
		if !ok {
			bad = append(bad, r.Nevra)
			continue
		}
		u := protocol.MissingUpdate{
			ID:             r.Name,
			Title:          r.Nevra,
			Classification: r.Type, // security, bugfix, enhancement...
			Severity:       r.Severity,
			Type:           "software",
			Package:        name,
			FixedVersion:   evr,
			Arch:           arch,
			Source:         SourceDnf,
		}
		if r.Buildtime > 0 {
			u.ReleasedAt = time.Unix(r.Buildtime, 0).UTC().Format("2006-01-02")
		}
		list = append(list, u)
	}
	if len(bad) > 0 {
		return list, fmt.Errorf("dnf advisory: %d NEVRA fora do formato (ex.: %q)", len(bad), bad[0])
	}
	return list, nil
}

// parseNEVRA quebra "openssl-libs-1:3.5.8-1.fc44.x86_64" em ("openssl-libs", "1:3.5.8-1.fc44", "x86_64").
// Da direita para a esquerda: arquitetura depois do último ponto, release depois do último
// hífen, versão (com época) antes dele. O nome pode ter hífens.
func parseNEVRA(s string) (name, evr, arch string, ok bool) {
	dot := strings.LastIndex(s, ".")
	if dot <= 0 {
		return "", "", "", false
	}
	rest, arch := s[:dot], s[dot+1:]
	rel := strings.LastIndex(rest, "-")
	if rel <= 0 {
		return "", "", "", false
	}
	ver := strings.LastIndex(rest[:rel], "-")
	if ver <= 0 {
		return "", "", "", false
	}
	return rest[:ver], rest[ver+1:], arch, true
}

// aptListsFreshness devolve a data da última atualização dos metadados do apt: o
// InRelease mais recente do pocket -security ou, sem ele, o mais recente de qualquer pocket.
func aptListsFreshness(listDir func(string) ([]string, error), modTime func(string) (time.Time, error)) (time.Time, bool) {
	names, err := listDir(aptListsDir)
	if err != nil {
		return time.Time{}, false
	}
	var security, any time.Time
	for _, n := range names {
		if !strings.HasSuffix(n, "_InRelease") {
			continue
		}
		t, err := modTime(aptListsDir + "/" + n)
		if err != nil {
			continue
		}
		if t.After(any) {
			any = t
		}
		if strings.Contains(n, "-security_") && t.After(security) {
			security = t
		}
	}
	switch {
	case !security.IsZero():
		return security.UTC(), true
	case !any.IsZero():
		return any.UTC(), true
	default:
		return time.Time{}, false
	}
}
