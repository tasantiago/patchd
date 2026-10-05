// Package msrc lê os documentos CVRF da API de atualizações de segurança da Microsoft
// (MSRC, https://api.msrc.microsoft.com/cvrf/v3.0) e os reduz ao que o patchd usa: os
// produtos, as CVEs (com severidade, impacto, exploração e CVSS) e as correções do
// fabricante (KB e build corrigido) por produto.
//
// Os números de tipo foram conferidos no documento real de 2026-Sep (Aula 6.1):
//
//	Threats:      0 = impacto, 1 = situação de exploração (uma por CVE), 3 = severidade
//	Remediations: 2 = correção do fabricante (KB, FixedBuild, Supercedence); 3 e 6 são
//	              links para notas da versão e artigos de suporte, sem dado novo
package msrc

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Tipos do CVRF que o patchd usa.
const (
	threatImpact        = 0
	threatExploitStatus = 1
	threatSeverity      = 3
	remediationVendor   = 2
)

// kbPattern: a descrição de uma correção do fabricante é o número do KB. Há entradas
// do tipo 2 com "Release Notes" no lugar do número: não são KBs e ficam de fora.
var kbPattern = regexp.MustCompile(`^[0-9]{6,8}$`)

// Update é um item da lista /updates: um documento, com a data da última revisão.
// Os documentos são revisados depois de publicados; a API não aceita requisição
// condicional (no-store, sem Last-Modified), então é por CurrentReleaseDate que a
// sincronização sabe o que baixar de novo.
type Update struct {
	ID                 string    `json:"ID"` // ex.: 2026-Sep
	DocumentTitle      string    `json:"DocumentTitle"`
	InitialReleaseDate time.Time `json:"InitialReleaseDate"`
	CurrentReleaseDate time.Time `json:"CurrentReleaseDate"`
	CvrfURL            string    `json:"CvrfUrl"`
}

// ParseUpdates lê a resposta de /updates.
func ParseUpdates(r io.Reader) ([]Update, error) {
	var resp struct {
		Value []Update `json:"value"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("lista de documentos do MSRC ilegível: %w", err)
	}
	return resp.Value, nil
}

// Document é um documento mensal já reduzido.
type Document struct {
	ID         string
	Title      string
	Initial    time.Time
	Current    time.Time
	Products   []Product
	Vulns      []Vuln
	Affected   []Affected
	Fixes      []Fix
	SkippedFix int // correções do tipo 2 sem número de KB (ex.: "Release Notes")
}

// Product é um produto do documento: o ID só vale dentro do catálogo do MSRC.
type Product struct {
	ID   string
	Name string
}

// Vuln é uma CVE.
type Vuln struct {
	CVE       string
	Title     string
	Exploited bool   // "Exploited:Yes"
	Disclosed bool   // "Publicly Disclosed:Yes"
	Exploit   string // o texto inteiro da situação de exploração, como veio
}

// Affected liga uma CVE a um produto, com a severidade, o impacto e o CVSS daquele produto.
type Affected struct {
	CVE       string
	ProductID string
	Severity  string  // Critical, Important, Moderate, Low
	Impact    string  // ex.: Elevation of Privilege
	BaseScore float64 // 0 quando não há CVSS para o produto
	Vector    string
}

// Fix é uma correção do fabricante para uma CVE num produto.
type Fix struct {
	CVE             string
	ProductID       string
	KB              string // só os dígitos
	SubType         string // Security Update, Monthly Rollup, Servicing Stack Update...
	FixedBuild      string // ex.: 10.0.26200.9445; vazio quando o produto não tem build
	Supersedes      string // KB substituído, como veio (pode ser vazio)
	RestartRequired string // Yes, No, Maybe, ou vazio
}

// Estrutura bruta do CVRF em JSON, só com os campos usados.
type rawDoc struct {
	DocumentTitle    value `json:"DocumentTitle"`
	DocumentTracking struct {
		Identification struct {
			ID value `json:"ID"`
		} `json:"Identification"`
		InitialReleaseDate cvrfTime `json:"InitialReleaseDate"`
		CurrentReleaseDate cvrfTime `json:"CurrentReleaseDate"`
	} `json:"DocumentTracking"`
	ProductTree struct {
		FullProductName []struct {
			ProductID string `json:"ProductID"`
			Value     string `json:"Value"`
		} `json:"FullProductName"`
	} `json:"ProductTree"`
	Vulnerability []rawVuln `json:"Vulnerability"`
}

type rawVuln struct {
	CVE             string `json:"CVE"`
	Title           value  `json:"Title"`
	ProductStatuses []struct {
		ProductID []string `json:"ProductID"`
	} `json:"ProductStatuses"`
	Threats []struct {
		Type        int      `json:"Type"`
		Description value    `json:"Description"`
		ProductID   []string `json:"ProductID"`
	} `json:"Threats"`
	CVSSScoreSets []struct {
		BaseScore float64  `json:"BaseScore"`
		Vector    string   `json:"Vector"`
		ProductID []string `json:"ProductID"`
	} `json:"CVSSScoreSets"`
	Remediations []struct {
		Type            int      `json:"Type"`
		SubType         string   `json:"SubType"`
		Description     value    `json:"Description"`
		FixedBuild      string   `json:"FixedBuild"`
		Supercedence    string   `json:"Supercedence"`
		RestartRequired value    `json:"RestartRequired"`
		ProductID       []string `json:"ProductID"`
	} `json:"Remediations"`
}

type value struct {
	Value string `json:"Value"`
}

// cvrfTime: as datas do documento vêm sem fuso ("2026-10-02T07:00:00"); as da lista,
// com "Z". As duas são UTC.
type cvrfTime time.Time

func (t *cvrfTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if v, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			*t = cvrfTime(v.UTC())
			return nil
		}
	}
	return fmt.Errorf("data fora do formato esperado: %q", s)
}

// Parse lê um documento CVRF e o reduz.
func Parse(r io.Reader) (Document, error) {
	var raw rawDoc
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Document{}, fmt.Errorf("documento do MSRC ilegível: %w", err)
	}
	d := Document{
		ID:      raw.DocumentTracking.Identification.ID.Value,
		Title:   raw.DocumentTitle.Value,
		Initial: time.Time(raw.DocumentTracking.InitialReleaseDate),
		Current: time.Time(raw.DocumentTracking.CurrentReleaseDate),
	}
	if d.ID == "" {
		return d, fmt.Errorf("documento do MSRC sem ID")
	}
	for _, p := range raw.ProductTree.FullProductName {
		d.Products = append(d.Products, Product{ID: p.ProductID, Name: p.Value})
	}

	for _, v := range raw.Vulnerability {
		vu := Vuln{CVE: v.CVE, Title: v.Title.Value}
		severity, impact := map[string]string{}, map[string]string{}
		for _, t := range v.Threats {
			switch t.Type {
			case threatExploitStatus:
				vu.Exploit = t.Description.Value
				st := parseExploitStatus(t.Description.Value)
				vu.Exploited = st["Exploited"] == "Yes"
				vu.Disclosed = st["Publicly Disclosed"] == "Yes"
			case threatSeverity:
				for _, id := range t.ProductID {
					severity[id] = t.Description.Value
				}
			case threatImpact:
				for _, id := range t.ProductID {
					impact[id] = t.Description.Value
				}
			}
		}
		d.Vulns = append(d.Vulns, vu)

		score, vector := map[string]float64{}, map[string]string{}
		for _, c := range v.CVSSScoreSets {
			for _, id := range c.ProductID {
				score[id], vector[id] = c.BaseScore, c.Vector
			}
		}
		seen := map[string]bool{}
		for _, s := range v.ProductStatuses {
			for _, id := range s.ProductID {
				if seen[id] {
					continue
				}
				seen[id] = true
				d.Affected = append(d.Affected, Affected{CVE: v.CVE, ProductID: id,
					Severity: severity[id], Impact: impact[id], BaseScore: score[id], Vector: vector[id]})
			}
		}

		for _, rm := range v.Remediations {
			if rm.Type != remediationVendor {
				continue
			}
			kb := strings.TrimSpace(rm.Description.Value)
			if !kbPattern.MatchString(kb) {
				d.SkippedFix++
				continue
			}
			for _, id := range rm.ProductID {
				d.Fixes = append(d.Fixes, Fix{CVE: v.CVE, ProductID: id, KB: kb, SubType: rm.SubType,
					FixedBuild: strings.TrimSpace(rm.FixedBuild), Supersedes: strings.TrimSpace(rm.Supercedence),
					RestartRequired: rm.RestartRequired.Value})
			}
		}
	}
	return d, nil
}

// parseExploitStatus lê "Publicly Disclosed:No;Exploited:No;Latest Software Release:...".
func parseExploitStatus(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
