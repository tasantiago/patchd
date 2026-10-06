// Package bodhi lê os updates de segurança do Fedora no Bodhi (https://bodhi.fedoraproject.org),
// o sistema que publica os updates de cada versão, e os reduz ao que o patchd usa: por update,
// os builds (NVR do pacote fonte, com a época) e as CVEs citadas nos bugs de segurança.
//
// Conferido na API real (Aula 6.2):
//
//	/releases/  as versões; as do Fedora têm id_prefix FEDORA e nome F<n>. EPEL, Flatpak e
//	            Container aparecem na mesma lista e ficam de fora
//	/updates/   paginado; type=security&status=stable&releases=F44. pushed_since filtra por
//	            date_pushed (>=); modified_since não serve: date_modified é null num update
//	            nunca editado
//	datas       "2026-10-05 01:05:36", sem fuso (UTC), ou null
//	CVEs        não há campo próprio: só no título dos bugs de segurança, cortados com "..."
//	            quando são muitas ("CVE-2026-102299 ... chromium: various flaws [fedora-all]")
package bodhi

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// fedoraName: as versões do Fedora propriamente dito (F43, F44), sem os sufixos de
// Container (F43C) e Flatpak (F44F).
var fedoraName = regexp.MustCompile(`^F[0-9]+$`)

// cvePattern acha as CVEs no título de um bug.
var cvePattern = regexp.MustCompile(`CVE-[0-9]{4}-[0-9]{4,}`)

// Release é uma versão do Bodhi.
type Release struct {
	Name     string `json:"name"`      // ex.: F44
	Version  string `json:"version"`   // ex.: 44
	IDPrefix string `json:"id_prefix"` // FEDORA, FEDORA-EPEL, FEDORA-FLATPAK...
	DistTag  string `json:"dist_tag"`  // ex.: f44
	State    string `json:"state"`     // current, pending, frozen, archived
}

// IsFedora diz se a versão é do Fedora propriamente dito.
func (r Release) IsFedora() bool { return r.IDPrefix == "FEDORA" && fedoraName.MatchString(r.Name) }

// ValidRelease diz se o nome tem a forma de uma versão do Fedora (F44).
func ValidRelease(name string) bool { return fedoraName.MatchString(name) }

// ReleasesPage é uma página da resposta de /releases/.
type ReleasesPage struct {
	Releases []Release `json:"releases"`
	Page     int       `json:"page"`
	Pages    int       `json:"pages"`
}

// ParseReleases lê uma página de /releases/.
func ParseReleases(r io.Reader) (ReleasesPage, error) {
	var p ReleasesPage
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return p, fmt.Errorf("lista de versões do Bodhi ilegível: %w", err)
	}
	return p, nil
}

// Update é um update de segurança já reduzido.
type Update struct {
	Alias    string // ex.: FEDORA-2026-35b989661c
	Release  string // ex.: F44
	Title    string
	Severity string    // urgent, high, medium, low, unspecified
	Pushed   time.Time // zero: nunca publicado
	Stable   time.Time
	Builds   []Build
	CVEs     []string
	// CVEsPartial: algum título de bug de segurança veio cortado ("..."); a lista de CVEs
	// do update está incompleta.
	CVEsPartial bool
}

// Build é um pacote fonte do update, já separado do NVR.
type Build struct {
	NVR     string // ex.: chromium-154.0.8037.92-1.fc44
	Name    string // pacote fonte: chromium
	Epoch   int
	Version string // 154.0.8037.92
	Release string // 1.fc44 (o "release" do rpm, não a versão do Fedora)
}

// UpdatesPage é uma página de /updates/ já reduzida.
type UpdatesPage struct {
	Updates []Update
	Page    int
	Pages   int
	Total   int
	// SkippedBuilds conta os builds que não são rpm (flatpak, container) ou cujo NVR não
	// tem a forma nome-versão-release.
	SkippedBuilds int
}

// bodhiTime lê as datas do Bodhi: "2006-01-02 15:04:05", sem fuso (UTC), ou null.
type bodhiTime struct{ time.Time }

func (t *bodhiTime) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return nil
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	v, err := time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return fmt.Errorf("data do Bodhi inválida %q", raw)
	}
	t.Time = v.UTC()
	return nil
}

type rawUpdates struct {
	Updates []struct {
		Alias    string `json:"alias"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Release  struct {
			Name string `json:"name"`
		} `json:"release"`
		DatePushed bodhiTime `json:"date_pushed"`
		DateStable bodhiTime `json:"date_stable"`
		Builds     []struct {
			NVR   string `json:"nvr"`
			Epoch *int   `json:"epoch"`
			Type  string `json:"type"`
		} `json:"builds"`
		Bugs []struct {
			Security bool   `json:"security"`
			Title    string `json:"title"`
		} `json:"bugs"`
	} `json:"updates"`
	Page  int `json:"page"`
	Pages int `json:"pages"`
	Total int `json:"total"`
}

// ParseUpdates lê uma página de /updates/. Os campos que o patchd não usa (comentários,
// karma, notas) são ignorados.
func ParseUpdates(r io.Reader) (UpdatesPage, error) {
	var raw rawUpdates
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return UpdatesPage{}, fmt.Errorf("lista de updates do Bodhi ilegível: %w", err)
	}
	p := UpdatesPage{Page: raw.Page, Pages: raw.Pages, Total: raw.Total}
	for _, ru := range raw.Updates {
		if ru.Alias == "" {
			return p, fmt.Errorf("update do Bodhi sem alias (%q)", ru.Title)
		}
		u := Update{
			Alias:    ru.Alias,
			Release:  ru.Release.Name,
			Title:    ru.Title,
			Severity: ru.Severity,
			Pushed:   ru.DatePushed.Time,
			Stable:   ru.DateStable.Time,
		}
		for _, b := range ru.Builds {
			name, version, release, ok := SplitNVR(b.NVR)
			if b.Type != "rpm" || !ok {
				p.SkippedBuilds++
				continue
			}
			epoch := 0
			if b.Epoch != nil {
				epoch = *b.Epoch
			}
			u.Builds = append(u.Builds, Build{NVR: b.NVR, Name: name, Epoch: epoch, Version: version, Release: release})
		}
		seen := map[string]bool{}
		for _, bug := range ru.Bugs {
			if !bug.Security {
				continue
			}
			if strings.Contains(bug.Title, "...") || strings.Contains(bug.Title, "…") {
				u.CVEsPartial = true
			}
			for _, c := range cvePattern.FindAllString(bug.Title, -1) {
				if !seen[c] {
					seen[c] = true
					u.CVEs = append(u.CVEs, c)
				}
			}
		}
		p.Updates = append(p.Updates, u)
	}
	return p, nil
}

// SplitNVR separa nome-versão-release pelos dois últimos hífens, como o rpm: o nome pode
// ter hífens ("python-requests"), a versão e o release não.
func SplitNVR(nvr string) (name, version, release string, ok bool) {
	r := strings.LastIndex(nvr, "-")
	if r <= 0 || r == len(nvr)-1 {
		return "", "", "", false
	}
	v := strings.LastIndex(nvr[:r], "-")
	if v <= 0 || v == r-1 {
		return "", "", "", false
	}
	return nvr[:v], nvr[v+1 : r], nvr[r+1:], true
}
