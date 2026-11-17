package winget

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Description é o que o painel usa para preencher o "Incluir programa": a versão mais nova do
// pacote, o nome e o fabricante (do manifesto de idioma padrão), os tipos de instalador x64 e,
// quando o pacote declara, como o programa aparece em Programas e Recursos.
type Description struct {
	ID, Version      string
	Name, Publisher  string   // PackageName e Publisher
	Types            []string // os tipos de instalador x64 (ou neutros), na ordem do manifesto
	DisplayName      string   // AppsAndFeaturesEntries[0].DisplayName, se houver
	DisplayPublisher string   // AppsAndFeaturesEntries[0].Publisher, se houver
	Unsupported      []string // os tipos que o patchd não instala (msix, zip...)
}

// Describe lê a versão mais nova do pacote: o manifesto de instalador e o de idioma padrão.
func (c *Client) Describe(ctx context.Context, id string) (Description, error) {
	d := Description{ID: id}
	versions, err := c.Versions(ctx, id)
	if err != nil {
		return d, err
	}
	d.Version = versions[0]
	m, err := c.Installer(ctx, id, d.Version)
	if err != nil {
		return d, err
	}
	for _, in := range m.Installers {
		if in.Architecture != "x64" && in.Architecture != "neutral" {
			continue
		}
		switch {
		case !Supported(in.Type):
			if !slices.Contains(d.Unsupported, in.Type) {
				d.Unsupported = append(d.Unsupported, in.Type)
			}
		case !slices.Contains(d.Types, in.Type):
			d.Types = append(d.Types, in.Type)
		}
	}
	path, _ := PackagePath(id)
	base := fmt.Sprintf("%s/%s/%s/%s", c.RawURL, path, url.PathEscape(d.Version), url.PathEscape(id))
	// O manifesto de versão diz o idioma padrão; o do idioma traz o nome e o fabricante.
	locale := "en-US"
	if data, err := c.get(ctx, base+".yaml", ""); err == nil {
		if v, err := parseYAML(data); err == nil {
			if root, ok := v.(map[string]any); ok && str(root, "DefaultLocale") != "" {
				locale = str(root, "DefaultLocale")
			}
		}
	}
	if data, err := c.get(ctx, base+".locale."+url.PathEscape(locale)+".yaml", ""); err == nil {
		if v, err := parseYAML(data); err == nil {
			if root, ok := v.(map[string]any); ok {
				d.Name, d.Publisher = oneLine(str(root, "PackageName")), oneLine(str(root, "Publisher"))
			}
		}
	}
	// AppsAndFeaturesEntries fica no manifesto de instalador (na raiz ou em cada instalador).
	if data, err := c.get(ctx, base+".installer.yaml", ""); err == nil {
		if v, err := parseYAML(data); err == nil {
			if root, ok := v.(map[string]any); ok {
				d.DisplayName, d.DisplayPublisher = appsAndFeatures(root)
			}
		}
	}
	return d, nil
}

// appsAndFeatures: o primeiro AppsAndFeaturesEntries com nome, na raiz ou num instalador.
func appsAndFeatures(root map[string]any) (name, publisher string) {
	first := func(m map[string]any) (string, string, bool) {
		list, _ := m["AppsAndFeaturesEntries"].([]any)
		for _, raw := range list {
			if e, ok := raw.(map[string]any); ok && str(e, "DisplayName") != "" {
				return oneLine(str(e, "DisplayName")), oneLine(str(e, "Publisher")), true
			}
		}
		return "", "", false
	}
	if n, p, ok := first(root); ok {
		return n, p
	}
	list, _ := root["Installers"].([]any)
	for _, raw := range list {
		if in, ok := raw.(map[string]any); ok {
			if n, p, ok := first(in); ok {
				return n, p
			}
		}
	}
	return "", ""
}

// oneLine: o texto de uma linha, sem espaços nas pontas, até 160 caracteres.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160])
	}
	return s
}
