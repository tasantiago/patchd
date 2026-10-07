// Package thirdparty avalia programas de terceiros (Chrome, Firefox, 7-Zip...) contra um
// manifesto: para cada programa e SO, como reconhecê-lo no inventário, em que categoria ele
// cai e de onde vem a versão de referência (Aula 6.5).
//
// O manifesto padrão vai embutido no binário (manifesto.json) e é genérico. Programas de
// cada organização (sistemas internos, versões mínimas definidas pela política) ficam num
// manifesto local, fora do repositório, que acrescenta regras ou substitui as do padrão.
package thirdparty

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

//go:embed manifesto.json
var embedded []byte

// Categorias de uma regra.
const (
	CategoryFeed     = "feed"      // versão de referência buscada numa fonte (fabricante, winget, Homebrew)
	CategoryMinimum  = "minimum"   // versão mínima definida no manifesto (política da organização)
	CategoryOSVendor = "os-vendor" // as correções vêm pelo catálogo do fabricante do SO (MSRC)
	CategoryEOL      = "eol"       // fora de suporte: não há versão alvo, há troca
	CategoryIgnore   = "ignore"    // entrada auxiliar de outro programa (não é avaliada)
)

// Tipos de fonte da versão de referência.
const (
	SourceChrome   = "chrome"   // versionhistory.googleapis.com; ID é a plataforma (win64, mac)
	SourceFirefox  = "firefox"  // product-details.mozilla.org; ID é a chave (LATEST_FIREFOX_VERSION, FIREFOX_ESR)
	SourceWinget   = "winget"   // github.com/microsoft/winget-pkgs; ID é o identificador do pacote
	SourceHomebrew = "homebrew" // formulae.brew.sh; ID é o cask
)

// Manifest é o conjunto de regras.
type Manifest struct {
	Version int   `json:"version"`
	Apps    []App `json:"apps"`
}

// App é a regra de um programa num SO.
type App struct {
	ID          string  `json:"id"`        // google-chrome
	OS          string  `json:"os"`        // windows ou macos
	Name        string  `json:"name"`      // rótulo
	Category    string  `json:"category"`  // feed, minimum, os-vendor, eol, ignore
	Publisher   string  `json:"publisher"` // expressão regular sobre o fabricante; vazia: qualquer
	Match       string  `json:"match"`     // expressão regular sobre o nome
	VersionFrom string  `json:"version_from,omitempty"`
	Source      *Source `json:"source,omitempty"`
	MinVersion  string  `json:"min_version,omitempty"`
	Note        string  `json:"note,omitempty"`

	publisher, match *regexp.Regexp
}

// Source diz de onde vem a versão de referência.
type Source struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (s Source) String() string { return s.Type + ":" + s.ID }

// Key identifica a regra: o mesmo programa tem uma regra por SO.
func (a App) Key() string { return a.ID + "/" + a.OS }

// Default devolve o manifesto embutido.
func Default() (Manifest, error) { return Parse(embedded) }

// Load devolve o manifesto embutido, acrescido do manifesto local quando path não é vazio.
func Load(path string) (Manifest, error) {
	m, err := Default()
	if err != nil || path == "" {
		return m, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("manifesto local: %w", err)
	}
	local, err := Parse(data)
	if err != nil {
		return m, fmt.Errorf("manifesto local %s: %w", path, err)
	}
	return m.Merge(local), nil
}

// Parse lê e valida um manifesto.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // campo com erro de digitação é erro, não regra ignorada
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifesto ilegível: %w", err)
	}
	if m.Version != 1 {
		return m, fmt.Errorf("manifesto na versão %d; este binário lê a versão 1", m.Version)
	}
	seen := map[string]bool{}
	for i := range m.Apps {
		a := &m.Apps[i]
		if err := a.compile(); err != nil {
			return m, fmt.Errorf("regra %d (%s): %w", i+1, a.Key(), err)
		}
		if seen[a.Key()] {
			return m, fmt.Errorf("regra %s repetida", a.Key())
		}
		seen[a.Key()] = true
	}
	return m, nil
}

func (a *App) compile() error {
	switch {
	case a.ID == "" || a.Match == "":
		return fmt.Errorf("id e match são obrigatórios")
	case a.OS != "windows" && a.OS != "macos":
		return fmt.Errorf("os %q: use windows ou macos", a.OS)
	case a.VersionFrom != "" && a.VersionFrom != "name":
		return fmt.Errorf("version_from %q: use \"name\" ou deixe vazio", a.VersionFrom)
	}
	switch a.Category {
	case CategoryFeed:
		if a.Source == nil || a.Source.ID == "" {
			return fmt.Errorf("categoria feed exige source com type e id")
		}
		switch a.Source.Type {
		case SourceChrome, SourceFirefox, SourceWinget, SourceHomebrew:
		default:
			return fmt.Errorf("fonte %q desconhecida (use chrome, firefox, winget ou homebrew)", a.Source.Type)
		}
	case CategoryMinimum:
		if _, ok := Normalize(a.MinVersion); !ok {
			return fmt.Errorf("categoria minimum exige min_version numérica (recebido %q)", a.MinVersion)
		}
	case CategoryOSVendor, CategoryEOL, CategoryIgnore:
	default:
		return fmt.Errorf("categoria %q desconhecida", a.Category)
	}
	var err error
	if a.match, err = regexp.Compile(a.Match); err != nil {
		return fmt.Errorf("match: %w", err)
	}
	if a.VersionFrom == "name" && a.match.NumSubexp() < 1 {
		return fmt.Errorf("version_from name exige um grupo no match, com a versão")
	}
	if a.Publisher != "" {
		if a.publisher, err = regexp.Compile(a.Publisher); err != nil {
			return fmt.Errorf("publisher: %w", err)
		}
	}
	return nil
}

// Merge devolve o manifesto com as regras de local: a regra com o mesmo id e SO substitui a
// do padrão; as outras são acrescentadas.
func (m Manifest) Merge(local Manifest) Manifest {
	out := Manifest{Version: m.Version}
	override := map[string]App{}
	for _, a := range local.Apps {
		override[a.Key()] = a
	}
	for _, a := range m.Apps {
		if o, ok := override[a.Key()]; ok {
			out.Apps = append(out.Apps, o)
			delete(override, a.Key())
			continue
		}
		out.Apps = append(out.Apps, a)
	}
	for _, a := range local.Apps {
		if _, ok := override[a.Key()]; ok {
			out.Apps = append(out.Apps, a)
		}
	}
	return out
}

// For devolve as regras de um SO, na ordem do manifesto.
func (m Manifest) For(osID string) []App {
	var out []App
	for _, a := range m.Apps {
		if a.OS == osID {
			out = append(out, a)
		}
	}
	return out
}

// Matches diz se a regra reconhece a entrada do inventário (nome e fabricante).
func (a App) Matches(name, publisher string) bool {
	if a.match == nil || !a.match.MatchString(name) {
		return false
	}
	return a.publisher == nil || a.publisher.MatchString(publisher)
}

// versionNumber: o começo numérico de uma versão ("140.17.0esr" → "140.17.0").
var versionNumber = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*`)

// Normalize reduz uma versão à parte numérica com pontos. Recusa o que não começa por um
// número ("Auto Updatable") e o que continua com outro número depois de um separador que
// não é ponto ("2022-12-02 15:42", uma data; "3.14-64", o "-64" do Python).
func Normalize(v string) (string, bool) {
	v = strings.TrimSpace(v)
	n := versionNumber.FindString(v)
	if n == "" {
		return "", false
	}
	rest := v[len(n):]
	if len(rest) > 1 && (rest[0] == '-' || rest[0] == ' ' || rest[0] == '_') && rest[1] >= '0' && rest[1] <= '9' {
		return "", false
	}
	return n, true
}

// InstalledVersion devolve a versão a comparar de uma entrada do inventário: a do campo de
// versão, ou a capturada do nome (version_from: name).
func (a App) InstalledVersion(name, version string) (string, bool) {
	if a.VersionFrom == "name" {
		m := a.match.FindStringSubmatch(name)
		if len(m) < 2 {
			return "", false
		}
		return Normalize(m[1])
	}
	return Normalize(version)
}
