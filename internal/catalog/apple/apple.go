// Package apple lê as duas fontes do macOS e as reduz ao que o patchd usa:
//
//   - gdmf.apple.com/v2/pmv (oficial, da Apple): as versões publicadas para instalação, com
//     build, e as melhorias de segurança em segundo plano ("26.3.1 (a)"). Não diz o que tem
//     suporte: lista do 11.7.11 ao 27.0.1, e as datas são da publicação no catálogo.
//   - SOFA (https://sofa.macadmins.io, comunitário, licença Apache 2.0): as versões de
//     segurança de cada major, com os builds, a data, o boletim da Apple e as CVEs (com
//     exploração ativa e presença no KEV). O SOFA lê os boletins em HTML da Apple
//     (support.apple.com/100100), que não têm versão em JSON.
//
// O gdmf diz se uma versão existe; o SOFA diz por que atualizar. Conferido nas respostas
// reais na Aula 6.2 (parte 1).
package apple

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

// GDMFVersion é uma versão do macOS publicada pela Apple.
type GDMFVersion struct {
	ProductVersion    string    // ex.: 26.7.1
	Build             string    // ex.: 25G241
	Extra             string    // "(a)" nas melhorias em segundo plano; vazio nas versões normais
	PrerequisiteBuild string    // build exigido pela melhoria em segundo plano
	Posting           time.Time // data de publicação no catálogo (não é a data da versão)
	Expiration        time.Time
	Devices           int // quantos modelos a recebem
}

type gdmfAsset struct {
	ProductVersion      string   `json:"ProductVersion"`
	ProductVersionExtra string   `json:"ProductVersionExtra"`
	Build               string   `json:"Build"`
	PrerequisiteBuild   string   `json:"PrerequisiteBuild"`
	PostingDate         string   `json:"PostingDate"`
	ExpirationDate      string   `json:"ExpirationDate"`
	SupportedDevices    []string `json:"SupportedDevices"`
}

// ParseGDMF lê a resposta do /v2/pmv e devolve as versões públicas do macOS, mais as
// melhorias de segurança em segundo plano. iOS e visionOS ficam de fora.
func ParseGDMF(r io.Reader) ([]GDMFVersion, error) {
	var raw struct {
		PublicAssetSets struct {
			MacOS []gdmfAsset `json:"macOS"`
		} `json:"PublicAssetSets"`
		PublicBackgroundSecurityImprovements struct {
			MacOS []gdmfAsset `json:"macOS"`
		} `json:"PublicBackgroundSecurityImprovements"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("resposta do gdmf ilegível: %w", err)
	}
	var out []GDMFVersion
	for _, a := range append(raw.PublicAssetSets.MacOS, raw.PublicBackgroundSecurityImprovements.MacOS...) {
		if a.ProductVersion == "" || a.Build == "" {
			return nil, fmt.Errorf("gdmf: versão sem número ou build: %+v", a)
		}
		v := GDMFVersion{
			ProductVersion:    a.ProductVersion,
			Build:             a.Build,
			Extra:             a.ProductVersionExtra,
			PrerequisiteBuild: a.PrerequisiteBuild,
			Devices:           len(a.SupportedDevices),
		}
		var err error
		if v.Posting, err = parseDay(a.PostingDate); err != nil {
			return nil, fmt.Errorf("gdmf %s: %w", a.ProductVersion, err)
		}
		if v.Expiration, err = parseDay(a.ExpirationDate); err != nil {
			return nil, fmt.Errorf("gdmf %s: %w", a.ProductVersion, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// GDMFHash resume o conteúdo das versões, sem depender da ordem nem da formatação da
// resposta: as versões são ordenadas e serializadas só com os campos que o patchd guarda.
// Muda quando muda uma versão, um build, uma data ou a quantidade de modelos.
func GDMFHash(vs []GDMFVersion) string {
	sorted := slices.Clone(vs)
	slices.SortFunc(sorted, func(a, b GDMFVersion) int {
		return strings.Compare(a.ProductVersion+"\x00"+a.Build+"\x00"+a.Extra, b.ProductVersion+"\x00"+b.Build+"\x00"+b.Extra)
	})
	data, _ := json.Marshal(sorted) // só tipos simples: não falha
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("data inválida %q", s)
	}
	return t, nil
}

// SOFAFeed é o feed do macOS do SOFA já reduzido.
type SOFAFeed struct {
	UpdateHash string // muda quando o conteúdo muda: é o que a sincronização compara
	Releases   []Release
}

// Release é uma versão de segurança do macOS.
type Release struct {
	Major          string    // como o SOFA chama a major: "Tahoe 26"
	MajorNumber    string    // "26"
	ProductVersion string    // "26.7.1"
	UpdateName     string    // "macOS Tahoe 26.7.1"
	Builds         []string  // vazio nas versões antigas, em que o SOFA não informa
	Date           time.Time // data da versão
	SecurityInfo   string    // URL do boletim da Apple
	CVEs           []CVE     // em ordem de ID
	UniqueCVEs     int       // a contagem do SOFA (igual a len(CVEs) no feed completo)
}

// CVE é uma CVE corrigida por uma versão.
type CVE struct {
	ID        string
	Exploited bool   // exploração ativa, segundo o boletim da Apple
	InKEV     bool   // presente no catálogo KEV da CISA
	Severity  string // a do SOFA (Critical, High...), vazia quando não informada
}

// majorNumber: o número no fim do nome da major ("Tahoe 26" → "26").
var majorNumber = regexp.MustCompile(`([0-9]+)$`)

// ParseSOFA lê o macos_data_feed.json (v2) do SOFA.
func ParseSOFA(r io.Reader) (SOFAFeed, error) {
	var raw struct {
		UpdateHash string `json:"UpdateHash"`
		OSVersions []struct {
			OSVersion        string `json:"OSVersion"`
			SecurityReleases []struct {
				UpdateName      string   `json:"UpdateName"`
				ProductName     string   `json:"ProductName"`
				ProductVersion  string   `json:"ProductVersion"`
				Build           string   `json:"Build"`
				AllBuilds       []string `json:"AllBuilds"`
				ReleaseDate     string   `json:"ReleaseDate"`
				SecurityInfo    string   `json:"SecurityInfo"`
				UniqueCVEsCount int      `json:"UniqueCVEsCount"`
				ActivelyExpl    []string `json:"ActivelyExploitedCVEs"`
				CVEs            map[string]struct {
					ActivelyExploited bool   `json:"ActivelyExploited"`
					InKEV             bool   `json:"InKEV"`
					Severity          string `json:"Severity"`
				} `json:"CVEs"`
			} `json:"SecurityReleases"`
		} `json:"OSVersions"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return SOFAFeed{}, fmt.Errorf("feed do SOFA ilegível: %w", err)
	}
	feed := SOFAFeed{UpdateHash: raw.UpdateHash}
	seen := map[string]bool{}
	for _, o := range raw.OSVersions {
		m := majorNumber.FindString(o.OSVersion)
		if m == "" {
			return feed, fmt.Errorf("SOFA: major sem número: %q", o.OSVersion)
		}
		for _, sr := range o.SecurityReleases {
			if sr.ProductVersion == "" {
				return feed, fmt.Errorf("SOFA: versão sem número na major %s", o.OSVersion)
			}
			if !strings.HasPrefix(sr.ProductVersion, m+".") && sr.ProductVersion != m {
				return feed, fmt.Errorf("SOFA: a versão %s não pertence à major %s", sr.ProductVersion, o.OSVersion)
			}
			if seen[sr.ProductVersion] {
				continue
			}
			seen[sr.ProductVersion] = true
			date, err := time.Parse(time.RFC3339, sr.ReleaseDate)
			if err != nil {
				return feed, fmt.Errorf("SOFA %s: data inválida %q", sr.ProductVersion, sr.ReleaseDate)
			}
			rel := Release{
				Major:          o.OSVersion,
				MajorNumber:    m,
				ProductVersion: sr.ProductVersion,
				UpdateName:     sr.UpdateName,
				Builds:         slices.Clone(sr.AllBuilds),
				Date:           date.UTC(),
				SecurityInfo:   sr.SecurityInfo,
				UniqueCVEs:     sr.UniqueCVEsCount,
			}
			if len(rel.Builds) == 0 && sr.Build != "" {
				rel.Builds = []string{sr.Build}
			}
			exploited := map[string]bool{}
			for _, c := range sr.ActivelyExpl {
				exploited[c] = true
			}
			for id, c := range sr.CVEs {
				rel.CVEs = append(rel.CVEs, CVE{ID: id, Exploited: c.ActivelyExploited || exploited[id], InKEV: c.InKEV, Severity: c.Severity})
				delete(exploited, id)
			}
			// Explorada citada fora do mapa de CVEs: entra assim mesmo.
			for id := range exploited {
				rel.CVEs = append(rel.CVEs, CVE{ID: id, Exploited: true})
			}
			slices.SortFunc(rel.CVEs, func(a, b CVE) int { return strings.Compare(a.ID, b.ID) })
			feed.Releases = append(feed.Releases, rel)
		}
	}
	return feed, nil
}
