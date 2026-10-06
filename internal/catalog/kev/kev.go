// Package kev lê o catálogo KEV (Known Exploited Vulnerabilities) da CISA: as CVEs
// comprovadamente exploradas, com data de inclusão, prazo e uso em ransomware.
//
// Conferido no feed real (Aula 6.3, parte 1): 1,8 MB, 12 campos presentes em todos os
// itens, "count" igual ao número de itens, "dateReleased" com fração de segundo e
// "catalogVersion" no formato AAAA.MM.DD. knownRansomwareCampaignUse é "Known" ou "Unknown".
//
// No patchd o KEV é a régua comum de exploração entre as fontes: a priorização considera
// explorada a CVE que está no KEV ou que o fabricante marca como explorada.
package kev

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

// cvePattern: o ID vira chave no banco e é cruzado com as outras fontes.
var cvePattern = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)

// Catalog é o KEV já reduzido.
type Catalog struct {
	Version  string // catalogVersion, ex.: 2026.10.04
	Released string // dateReleased, como veio: é o que a sincronização compara
	Vulns    []Vuln
}

// Vuln é uma CVE do KEV.
type Vuln struct {
	CVE        string
	Vendor     string // vendorProject
	Product    string
	Name       string // vulnerabilityName
	Added      time.Time
	Due        time.Time // prazo da CISA para as agências federais americanas
	Ransomware bool      // knownRansomwareCampaignUse == "Known"
	CWEs       []string
}

// Parse lê o feed JSON do KEV e confere a integridade: o "count" do cabeçalho tem de bater
// com o número de itens, e nenhuma CVE pode aparecer duas vezes. Um feed que não confere é
// recusado inteiro: substituir o catálogo bom por um pedaço dele esconderia CVEs exploradas.
func Parse(r io.Reader) (Catalog, error) {
	var raw struct {
		CatalogVersion  string `json:"catalogVersion"`
		DateReleased    string `json:"dateReleased"`
		Count           int    `json:"count"`
		Vulnerabilities []struct {
			CVEID                      string   `json:"cveID"`
			VendorProject              string   `json:"vendorProject"`
			Product                    string   `json:"product"`
			VulnerabilityName          string   `json:"vulnerabilityName"`
			DateAdded                  string   `json:"dateAdded"`
			DueDate                    string   `json:"dueDate"`
			KnownRansomwareCampaignUse string   `json:"knownRansomwareCampaignUse"`
			CWEs                       []string `json:"cwes"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Catalog{}, fmt.Errorf("feed do KEV ilegível: %w", err)
	}
	if raw.DateReleased == "" || raw.CatalogVersion == "" {
		return Catalog{}, fmt.Errorf("feed do KEV sem catalogVersion ou dateReleased")
	}
	if raw.Count != len(raw.Vulnerabilities) || raw.Count == 0 {
		return Catalog{}, fmt.Errorf("feed do KEV incompleto: count %d, itens %d", raw.Count, len(raw.Vulnerabilities))
	}
	c := Catalog{Version: raw.CatalogVersion, Released: raw.DateReleased}
	seen := make(map[string]bool, len(raw.Vulnerabilities))
	for _, v := range raw.Vulnerabilities {
		if !cvePattern.MatchString(v.CVEID) {
			return Catalog{}, fmt.Errorf("KEV: ID fora do formato: %q", v.CVEID)
		}
		if seen[v.CVEID] {
			return Catalog{}, fmt.Errorf("KEV: %s aparece duas vezes", v.CVEID)
		}
		seen[v.CVEID] = true
		added, err := time.Parse("2006-01-02", v.DateAdded)
		if err != nil {
			return Catalog{}, fmt.Errorf("KEV %s: dateAdded inválida %q", v.CVEID, v.DateAdded)
		}
		var due time.Time
		if v.DueDate != "" {
			if due, err = time.Parse("2006-01-02", v.DueDate); err != nil {
				return Catalog{}, fmt.Errorf("KEV %s: dueDate inválida %q", v.CVEID, v.DueDate)
			}
		}
		cwes := v.CWEs
		if cwes == nil {
			cwes = []string{}
		}
		c.Vulns = append(c.Vulns, Vuln{
			CVE:        v.CVEID,
			Vendor:     v.VendorProject,
			Product:    v.Product,
			Name:       v.VulnerabilityName,
			Added:      added,
			Due:        due,
			Ransomware: v.KnownRansomwareCampaignUse == "Known",
			CWEs:       cwes,
		})
	}
	return c, nil
}
