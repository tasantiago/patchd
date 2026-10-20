// Package sbom monta a lista de componentes (SBOM) de um executável Go no formato
// CycloneDX 1.5 (JSON), a partir do que o próprio executável grava no build: o módulo
// principal, cada módulo de terceiros com a versão e o hash do go.sum, a versão do Go e as
// opções do build (Aula 20.8, S9). Sem ferramenta de fora: o que está na lista é exatamente
// o que foi compilado, inclusive os replace do go.mod.
//
// Determinístico: o mesmo executável dá o mesmo arquivo (o número de série sai do SHA-256 do
// executável e a data, do commit), para o pacote de revisão poder ser conferido de novo.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
)

// BOM é o documento CycloneDX (só os campos que usamos).
type BOM struct {
	BOMFormat    string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	SerialNumber string       `json:"serialNumber"`
	Version      int          `json:"version"`
	Metadata     Metadata     `json:"metadata"`
	Components   []Component  `json:"components"`
	Dependencies []Dependency `json:"dependencies"`
}

// Metadata: quem gerou, quando e de qual executável.
type Metadata struct {
	Timestamp string    `json:"timestamp,omitempty"`
	Tools     Tools     `json:"tools"`
	Component Component `json:"component"`
}

// Tools: a ferramenta que gerou o documento.
type Tools struct {
	Components []Component `json:"components"`
}

// Component é o executável, a biblioteca padrão ou um módulo.
type Component struct {
	Type       string     `json:"type"`
	BOMRef     string     `json:"bom-ref,omitempty"`
	Name       string     `json:"name"`
	Version    string     `json:"version,omitempty"`
	PURL       string     `json:"purl,omitempty"`
	Hashes     []Hash     `json:"hashes,omitempty"`
	Properties []Property `json:"properties,omitempty"`
}

// Hash de um componente.
type Hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// Property é um par nome/valor livre (as opções do build, o hash do go.sum, o replace).
type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Dependency liga o executável aos componentes dele. O executável não grava o grafo entre
// os módulos, só a lista: tudo fica pendurado no executável.
type Dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ErrModified: executável compilado com alterações não commitadas (não corresponde a commit
// nenhum, e a revisão não teria como refazer).
var ErrModified = errors.New("compilado com alterações não commitadas (vcs.modified=true)")

// Artifact é o executável analisado.
type Artifact struct {
	Name    string // nome do arquivo, sem a pasta
	Version string // versão do patchd (o build a injeta com -X, que o executável não grava)
	SHA256  string // hexadecimal
}

// Sum devolve o SHA-256 em hexadecimal.
func Sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Build monta o documento. tool é a versão do gerador (vai em metadata.tools).
func Build(art Artifact, bi *debug.BuildInfo, tool string) (BOM, error) {
	if bi == nil {
		return BOM{}, errors.New("sem informação de build")
	}
	settings := map[string]string{}
	for _, s := range bi.Settings {
		settings[s.Key] = s.Value
	}
	if settings["vcs.modified"] == "true" {
		return BOM{}, ErrModified
	}
	version := art.Version
	if version == "" {
		version = bi.Main.Version
	}
	main := Component{Type: "application", Name: art.Name, Version: version,
		PURL: purl(bi.Main.Path, version)}
	main.BOMRef = main.PURL + "#" + art.Name // um por executável, mesmo com o mesmo módulo
	if art.SHA256 != "" {
		main.Hashes = []Hash{{Alg: "SHA-256", Content: art.SHA256}}
	}
	main.Properties = append(main.Properties, Property{"golang:module", bi.Main.Path}, Property{"golang:goversion", bi.GoVersion})
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// -ldflags pode levar valores do ambiente do build: fica de fora (o -trimpath já não o grava).
		if k == "-ldflags" {
			continue
		}
		main.Properties = append(main.Properties, Property{"golang:build:" + k, settings[k]})
	}

	goVer := strings.TrimPrefix(bi.GoVersion, "go")
	std := Component{Type: "library", Name: "stdlib", Version: goVer, PURL: "pkg:golang/stdlib@" + escVersion(goVer)}
	std.BOMRef = std.PURL
	comps := []Component{std}
	for _, d := range bi.Deps {
		c := Component{Type: "library", Name: d.Path, Version: d.Version}
		switch r := d.Replace; {
		case r == nil:
			if d.Sum != "" {
				c.Properties = append(c.Properties, Property{"golang:sum", d.Sum})
			}
		case localPath(r.Path):
			// replace por pasta local: a versão do go.mod não é o que foi compilado, e a
			// pasta é do host do build (não vai para o documento).
			c.Properties = append(c.Properties, Property{"golang:replace", "pasta local"})
		default:
			c.Name, c.Version = r.Path, r.Version
			c.Properties = append(c.Properties, Property{"golang:replaces", d.Path + "@" + d.Version})
			if r.Sum != "" {
				c.Properties = append(c.Properties, Property{"golang:sum", r.Sum})
			}
		}
		c.PURL = purl(c.Name, c.Version)
		c.BOMRef = c.PURL
		comps = append(comps, c)
	}
	sort.SliceStable(comps[1:], func(i, j int) bool { return comps[1+i].Name < comps[1+j].Name })

	refs := make([]string, len(comps))
	for i, c := range comps {
		refs[i] = c.BOMRef
	}
	b := BOM{
		BOMFormat: "CycloneDX", SpecVersion: "1.5", Version: 1,
		SerialNumber: serial(art.SHA256, art.Name),
		Metadata: Metadata{
			Timestamp: settings["vcs.time"],
			Tools:     Tools{Components: []Component{{Type: "application", Name: "patchd-sbom", Version: tool}}},
			Component: main,
		},
		Components:   comps,
		Dependencies: []Dependency{{Ref: main.BOMRef, DependsOn: refs}},
	}
	return b, nil
}

// localPath: replace por pasta ("./x", "../x", "/x", "C:\\x"), não por outro módulo.
func localPath(p string) bool {
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		len(p) > 1 && p[1] == ':'
}

// purl: pkg:golang/<caminho do módulo>@<versão>.
func purl(path, version string) string {
	p := "pkg:golang/" + path
	if version != "" && version != "(devel)" {
		p += "@" + escVersion(version)
	}
	return p
}

// escVersion: o "+" (como em +incompatible) é codificado no purl.
func escVersion(v string) string { return strings.ReplaceAll(v, "+", "%2B") }

// serial: urn:uuid estável, derivado do executável (formato de UUID versão 5).
func serial(sha, name string) string {
	h := sha256.Sum256([]byte("patchd-sbom\x00" + sha + "\x00" + name))
	u := h[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	x := hex.EncodeToString(u)
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", x[0:8], x[8:12], x[12:16], x[16:20], x[20:32])
}

// Module é uma linha do resumo: um módulo e os executáveis que o usam.
type Module struct {
	Name, Version string
	In            []string
}

// Modules junta os componentes de vários documentos, para o resumo legível do pacote.
func Modules(boms []BOM) []Module {
	idx := map[string]*Module{}
	for _, b := range boms {
		for _, c := range b.Components {
			k := c.Name + "@" + c.Version
			m := idx[k]
			if m == nil {
				m = &Module{Name: c.Name, Version: c.Version}
				idx[k] = m
			}
			m.In = append(m.In, b.Metadata.Component.Name)
		}
	}
	out := make([]Module, 0, len(idx))
	for _, m := range idx {
		sort.Strings(m.In)
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}
