// Package osv lê registros no formato OSV (https://ossf.github.io/osv-schema/) do bucket
// público do OSV.dev e os reduz ao que o patchd usa: por ecossistema e pacote fonte, a
// versão que corrige; e as CVEs, com a prioridade da distribuição.
//
// Conferido nos dados reais do Ubuntu (Aula 6.2):
//
//	modified_id.csv  "<data ISO com nanossegundos>,<ID>", do mais recente para o mais antigo;
//	                 lista todos os registros do ecossistema (USN-*, UBUNTU-CVE-*, LSN-*)
//	affected[]       um por ecossistema e pacote fonte ("Ubuntu:26.04:LTS", "openssl");
//	                 ranges do tipo ECOSYSTEM com eventos introduced e fixed
//	database_specific.cves_map (dentro de cada affected): as CVEs daquele ecossistema,
//	                 com severidade do tipo "Ubuntu" (prioridade) e CVSS_V3 (vetor)
//
// As versões são guardadas exatamente como vêm, com a época ("1:3.5.17-1ubuntu0.26.04.2"):
// a comparação segue as regras do dpkg e entra na Aula 6.4.
package osv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Entry é uma linha do modified_id.csv. Modified fica como texto, exatamente como veio:
// tem nanossegundos, e o timestamptz do PostgreSQL guarda microssegundos. Guardado como
// data, nunca mais seria igual ao da lista, e o registro seria baixado em toda sincronização.
type Entry struct {
	ID       string
	Modified string
}

// ParseModifiedIDs lê o modified_id.csv de um ecossistema e devolve as linhas cujo ID começa
// com prefix (vazio: todas), na ordem do arquivo. Uma linha repetida fica com a primeira
// ocorrência, que é a mais recente.
func ParseModifiedIDs(r io.Reader, prefix string) ([]Entry, error) {
	var out []Entry
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ts, id, ok := strings.Cut(line, ",")
		if !ok || id == "" {
			return nil, fmt.Errorf("modified_id.csv, linha %d fora do formato: %q", n, line)
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, fmt.Errorf("modified_id.csv, linha %d: data inválida %q", n, ts)
		}
		if !strings.HasPrefix(id, prefix) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Entry{ID: id, Modified: ts})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("modified_id.csv: %w", err)
	}
	return out, nil
}

// Record é um registro OSV já reduzido.
type Record struct {
	ID        string
	Summary   string
	Published time.Time
	Modified  time.Time
	Withdrawn time.Time // zero: o registro vale
	Fixes     []Fix
	CVEs      []CVE
	// Unfixed conta os pacotes afetados sem evento "fixed" no registro: vulneráveis e sem
	// correção. Numa USN não deveria acontecer (o aviso existe porque há correção).
	Unfixed int
}

// Fix diz que, no ecossistema, o pacote fonte está corrigido a partir de Version.
type Fix struct {
	Ecosystem string // ex.: Ubuntu:26.04:LTS
	Package   string // pacote fonte, ex.: openssl
	Version   string // ex.: 3.5.5-1ubuntu3.7
}

// CVE é uma CVE de um ecossistema do registro.
type CVE struct {
	Ecosystem string
	ID        string
	Priority  string // prioridade da distribuição (no Ubuntu: negligible, low, medium, high, critical)
	CVSS      string // vetor CVSS; vazio quando o registro não informa
}

type rawRecord struct {
	ID        string     `json:"id"`
	Summary   string     `json:"summary"`
	Published time.Time  `json:"published"`
	Modified  time.Time  `json:"modified"`
	Withdrawn *time.Time `json:"withdrawn"`
	Affected  []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
		DatabaseSpecific struct {
			CVEsMap struct {
				Ecosystem string `json:"ecosystem"`
				CVEs      []struct {
					ID       string `json:"id"`
					Severity []struct {
						Type  string `json:"type"`
						Score string `json:"score"`
					} `json:"severity"`
				} `json:"cves"`
			} `json:"cves_map"`
		} `json:"database_specific"`
	} `json:"affected"`
}

// Parse lê um registro OSV. Os campos que o patchd não usa (details, versions, os binários)
// são ignorados: a lista "versions" de uma USN do kernel tem milhares de itens.
func Parse(r io.Reader) (Record, error) {
	var raw rawRecord
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Record{}, fmt.Errorf("registro OSV ilegível: %w", err)
	}
	if raw.ID == "" {
		return Record{}, fmt.Errorf("registro OSV sem id")
	}
	rec := Record{
		ID:        raw.ID,
		Summary:   raw.Summary,
		Published: raw.Published.UTC(),
		Modified:  raw.Modified.UTC(),
	}
	if raw.Withdrawn != nil {
		rec.Withdrawn = raw.Withdrawn.UTC()
	}

	seenCVE := map[[2]string]bool{}
	for _, a := range raw.Affected {
		eco, pkg := a.Package.Ecosystem, a.Package.Name
		if eco == "" || pkg == "" {
			continue
		}
		fixed := 0
		for _, rg := range a.Ranges {
			// SEMVER e GIT não se aplicam a pacotes de distribuição.
			if rg.Type != "ECOSYSTEM" {
				continue
			}
			for _, ev := range rg.Events {
				if ev.Fixed != "" {
					rec.Fixes = append(rec.Fixes, Fix{Ecosystem: eco, Package: pkg, Version: ev.Fixed})
					fixed++
				}
			}
		}
		if fixed == 0 {
			rec.Unfixed++
		}

		// O mapa de CVEs se repete em cada pacote do mesmo ecossistema: fica a primeira vez.
		cm := a.DatabaseSpecific.CVEsMap
		cveEco := cm.Ecosystem
		if cveEco == "" {
			cveEco = eco
		}
		for _, c := range cm.CVEs {
			k := [2]string{cveEco, c.ID}
			if c.ID == "" || seenCVE[k] {
				continue
			}
			seenCVE[k] = true
			cve := CVE{Ecosystem: cveEco, ID: c.ID}
			for _, s := range c.Severity {
				switch {
				case s.Type == "Ubuntu" && cve.Priority == "":
					cve.Priority = s.Score
				case strings.HasPrefix(s.Type, "CVSS_") && cve.CVSS == "":
					cve.CVSS = s.Score
				}
			}
			rec.CVEs = append(rec.CVEs, cve)
		}
	}
	return rec, nil
}
