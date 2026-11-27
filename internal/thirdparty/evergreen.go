package thirdparty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/version"
)

// O Evergreen como fonte do link do instalador: além da versão, cada linha da resposta traz o
// link (URI) e, em muitos aplicativos, o SHA-256 do arquivo. O servidor escolhe a linha que
// passa no filtro, é do tipo do instalador (msi ou exe) e tem a versão pedida (vazia: a mais
// nova), e baixa o link dela.

// EvergreenInstaller é a linha escolhida: o instalador de uma versão.
type EvergreenInstaller struct {
	Version, URI, SHA256, Type, Architecture string
}

// EvergreenInstaller lê o aplicativo no Evergreen e escolhe o instalador (PickEvergreen).
func (c *Client) EvergreenInstaller(ctx context.Context, app string, filter map[string]string, installerType, ver string) (EvergreenInstaller, error) {
	if !evergreenID.MatchString(app) {
		return EvergreenInstaller{}, fmt.Errorf("evergreen: o aplicativo %q é o nome da lista do Evergreen (AdobeAcrobatReaderDC)", app)
	}
	var raw json.RawMessage
	if err := c.getJSON(ctx, fmt.Sprintf("%s/app/%s", c.EvergreenURL, url.PathEscape(app)), &raw); err != nil {
		return EvergreenInstaller{}, err
	}
	rows, err := evergreenRows(raw)
	if err != nil {
		return EvergreenInstaller{}, err
	}
	return PickEvergreen(rows, filter, installerType, ver)
}

var sha256Hex = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)

// evergreenFamily: o Type da linha do Evergreen para o tipo do instalador do patchd (o MSI é
// "msi"; nullsoft, inno, burn e o exe genérico são "exe").
func evergreenFamily(installerType string) string {
	if installerType == "msi" || installerType == "wix" {
		return "msi"
	}
	return "exe"
}

// PickEvergreen escolhe a linha: passa no filtro, é do tipo (pelo campo Type ou, sem ele,
// pela extensão do link), tem link https e a versão pedida (comparada pela parte numérica;
// vazia, a mais nova). Entre as que sobram, a de 64 bits (Architecture x64) quando o campo
// existe; duas linhas com links diferentes ainda: o filtro precisa ser mais fino.
func PickEvergreen(rows []map[string]any, filter map[string]string, installerType, ver string) (EvergreenInstaller, error) {
	family := evergreenFamily(installerType)
	var cands []EvergreenInstaller
	for _, row := range rows {
		if !rowMatches(row, filter) {
			continue
		}
		in := EvergreenInstaller{}
		in.Version, _ = field(row, "Version")
		in.URI, _ = field(row, "URI")
		in.SHA256, _ = field(row, "Sha256")
		in.Architecture, _ = field(row, "Architecture")
		typ, hasType := field(row, "Type")
		u, err := url.Parse(in.URI)
		if in.Version == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			continue
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
		if hasType {
			if !strings.EqualFold(typ, family) {
				continue
			}
		} else if ext != family {
			continue
		}
		in.Type = family
		if !sha256Hex.MatchString(in.SHA256) {
			in.SHA256 = ""
		}
		in.SHA256 = strings.ToLower(in.SHA256)
		cands = append(cands, in)
	}
	if len(cands) == 0 {
		return EvergreenInstaller{}, fmt.Errorf("nenhuma linha do Evergreen passa no filtro com instalador %s e link https", family)
	}
	norm := func(v string) string {
		n, ok := Normalize(v)
		if !ok {
			return v
		}
		return n
	}
	want := norm(ver)
	if ver == "" {
		for _, c := range cands {
			if want == "" {
				want = norm(c.Version)
				continue
			}
			if r, err := version.CompareDotted(norm(c.Version), want); err == nil && r > 0 {
				want = norm(c.Version)
			}
		}
	}
	equal := func(a, b string) bool {
		if r, err := version.CompareDotted(a, b); err == nil {
			return r == 0 // 2.0 e 2.0.0 são a mesma
		}
		return a == b
	}
	var same []EvergreenInstaller
	for _, c := range cands {
		if equal(norm(c.Version), want) {
			same = append(same, c)
		}
	}
	if len(same) == 0 {
		var have []string
		for _, c := range cands {
			if !slices.Contains(have, c.Version) {
				have = append(have, c.Version)
			}
		}
		return EvergreenInstaller{}, fmt.Errorf("o Evergreen não tem a versão %s (tem: %s): ele só lista as versões atuais", ver, strings.Join(have, ", "))
	}
	if i := slices.IndexFunc(same, func(c EvergreenInstaller) bool { return strings.EqualFold(c.Architecture, "x64") }); i >= 0 {
		x64 := same[:0:0]
		for _, c := range same {
			if strings.EqualFold(c.Architecture, "x64") {
				x64 = append(x64, c)
			}
		}
		same = x64
	}
	for _, c := range same[1:] {
		if c.URI != same[0].URI {
			return EvergreenInstaller{}, errors.New("mais de uma linha do Evergreen sobra para a versão: refine o filtro (ex.: Channel=Stable; Architecture=x64)")
		}
	}
	return same[0], nil
}

// ParseEvergreenFilter lê o filtro como o painel o escreve: "Campo=valor", separados por ";"
// ou por linha. Vazio: nil.
func ParseEvergreenFilter(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !filterKey.MatchString(k) || v == "" || len(v) > 60 || strings.ContainsAny(v, "\t[],=") {
			return nil, fmt.Errorf("filtro do Evergreen %q: use Campo=valor (ex.: Architecture=x64; Channel=Stable)", part)
		}
		out[k] = v
	}
	if len(out) > maxFilterPairs {
		return nil, fmt.Errorf("filtro do Evergreen: no máximo %d campos", maxFilterPairs)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// FormatEvergreenFilter escreve o filtro como o painel mostra ("Architecture=x64; Channel=Stable").
func FormatEvergreenFilter(f map[string]string) string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		keys[i] = k + "=" + f[k]
	}
	return strings.Join(keys, "; ")
}

// ValidEvergreenApp: o nome do aplicativo na lista do Evergreen.
func ValidEvergreenApp(app string) bool { return evergreenID.MatchString(app) }

// EvergreenApp é um aplicativo da lista do Evergreen (o /apps da API): o nome que as consultas
// usam, o nome do programa e a página do fabricante.
type EvergreenApp struct {
	Name, Application, Link string
}

// EvergreenApps lê a lista de aplicativos do Evergreen.
func (c *Client) EvergreenApps(ctx context.Context) ([]EvergreenApp, error) {
	var raw []map[string]any
	if err := c.getJSON(ctx, c.EvergreenURL+"/apps", &raw); err != nil {
		return nil, err
	}
	out := make([]EvergreenApp, 0, len(raw))
	for _, row := range raw {
		a := EvergreenApp{}
		a.Name, _ = field(row, "Name")
		a.Application, _ = field(row, "Application")
		a.Link, _ = field(row, "Link")
		if evergreenID.MatchString(a.Name) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("a lista de aplicativos do Evergreen veio vazia")
	}
	return out, nil
}

// FindEvergreenApps: os aplicativos cujo nome ou nome do programa contém o texto (sem
// diferença de maiúsculas), o de nome igual primeiro, no máximo max.
func FindEvergreenApps(apps []EvergreenApp, q string, max int) []EvergreenApp {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var exact, rest []EvergreenApp
	for _, a := range apps {
		switch {
		case strings.ToLower(a.Name) == q:
			exact = append(exact, a)
		case strings.Contains(strings.ToLower(a.Name), q) || strings.Contains(strings.ToLower(a.Application), q):
			rest = append(rest, a)
		}
	}
	out := append(exact, rest...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// EvergreenRow é uma linha da resposta para mostrar: a versão, a arquitetura, o tipo, o link,
// se traz o SHA-256 e os outros campos (canal, idioma...) como "Campo=valor".
type EvergreenRow struct {
	Version, Architecture, Type, URI string
	HasSHA                           bool
	Other                            []string
}

// EvergreenRowsOf lê as linhas do aplicativo, para a busca do painel.
func (c *Client) EvergreenRowsOf(ctx context.Context, app string) ([]map[string]any, error) {
	if !evergreenID.MatchString(app) {
		return nil, fmt.Errorf("o aplicativo %q: o nome da lista do Evergreen (ex.: Handbrake)", app)
	}
	var raw json.RawMessage
	if err := c.getJSON(ctx, fmt.Sprintf("%s/app/%s", c.EvergreenURL, url.PathEscape(app)), &raw); err != nil {
		return nil, err
	}
	return evergreenRows(raw)
}

// EvergreenRowsView: as linhas que passam no filtro, para a tela (até max).
func EvergreenRowsView(rows []map[string]any, filter map[string]string, max int) []EvergreenRow {
	var out []EvergreenRow
	known := map[string]bool{"version": true, "architecture": true, "type": true, "uri": true, "sha256": true, "size": true, "date": true, "startdate": true}
	for _, row := range rows {
		if !rowMatches(row, filter) {
			continue
		}
		r := EvergreenRow{}
		r.Version, _ = field(row, "Version")
		r.Architecture, _ = field(row, "Architecture")
		r.Type, _ = field(row, "Type")
		r.URI, _ = field(row, "URI")
		sha, _ := field(row, "Sha256")
		r.HasSHA = sha256Hex.MatchString(sha)
		for k, v := range row {
			if !known[strings.ToLower(k)] && v != nil {
				r.Other = append(r.Other, k+"="+fmt.Sprint(v))
			}
		}
		slices.Sort(r.Other)
		out = append(out, r)
		if len(out) == max {
			break
		}
	}
	return out
}
