package choco

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/winget"
)

// Description é o que o formulário "Incluir programa › Do Chocolatey" aproveita do pacote: o
// nome, a versão, o link, o tipo do instalador e os argumentos, e como o programa aparece em
// Programas e Recursos. São palpites tirados do script (sem executá-lo): o painel mostra e
// quem inclui confere.
type Description struct {
	ID, Title, Version, Authors string
	URL, SHA256, URLVar         string
	// Type: msi, nullsoft, inno ou exe, pelo fileType e pelo silentArgs do script.
	Type string
	// Arguments: os do silentArgs que o job aceita, quando não são o padrão do tipo (vazio: o
	// padrão). Os que o job recusa (o /l*v com o caminho do log) ficam em Dropped.
	Arguments string
	Dropped   []string
	// SilentArgs: o silentArgs do script como está ("" quando o script não o tem em texto).
	SilentArgs string
	// DisplayName: o softwareName do script sem o curinga final ("Mozilla Firefox*" →
	// "Mozilla Firefox", Prefix true); vazio quando o script não diz.
	DisplayName string
	Prefix      bool
	Embedded    []string
}

// silentRaw: o silentArgs entre aspas duplas com variáveis ("/qn /l*v `"$($env:TEMP)\x.log`""),
// que as atribuições de texto não pegam.
var silentRaw = regexp.MustCompile(`(?mi)^[ \t]*\$?silentArgs[ \t]*=[ \t]*(["'])(.*)(["'])[ \t]*(?:#.*)?$`)

// Describe lê do pacote o que o formulário preenche. Erro: o script não tem o link (o pacote
// embutido, ou o link em outra variável).
func Describe(p Package) (Description, error) {
	d := Description{ID: p.ID, Title: p.Title, Version: ProgramVersion(p.Version), Authors: p.Authors, Embedded: p.Embedded}
	in, err := Extract(p.Script, "", "")
	if err != nil {
		return d, err
	}
	d.URL, d.SHA256, d.URLVar = in.URL, in.SHA256, in.URLVar
	vals := values(p.Script)
	d.SilentArgs = strings.TrimSpace(cmpOr(vals["silentargs"], vals["silentargs64"]))
	if m := silentRaw.FindStringSubmatch(p.Script); d.SilentArgs == "" && m != nil {
		d.SilentArgs = strings.TrimSpace(m[2]) // com $ e crases dentro: como está, para mostrar
	}
	d.Type, d.Arguments, d.Dropped = guessType(strings.ToLower(vals["filetype"]), d.URL, d.SilentArgs)
	if name := strings.TrimSpace(vals["softwarename"]); name != "" {
		if i := strings.IndexAny(name, "*?"); i >= 0 {
			name, d.Prefix = strings.TrimSpace(name[:i]), true
		}
		d.DisplayName = name
	}
	return d, nil
}

// guessType: o tipo do instalador e os argumentos. fileType vazio: a extensão do link.
func guessType(fileType, link, silent string) (typ, args string, dropped []string) {
	if fileType == "" {
		fileType = strings.TrimPrefix(strings.ToLower(path.Ext(strings.SplitN(link, "?", 2)[0])), ".")
	}
	tokens := strings.Fields(silent)
	has := func(want ...string) bool {
		return slices.ContainsFunc(tokens, func(t string) bool {
			return slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(t, w) })
		})
	}
	switch {
	case fileType == "msi":
		typ = "msi"
	case has("/VERYSILENT", "/SILENT"):
		typ = "inno"
	case slices.Contains(tokens, "/S"):
		typ = "nullsoft"
	default:
		typ = "exe"
	}
	var kept []string
	for _, t := range tokens {
		if _, err := jobs.SplitArgs(typ, t); err != nil {
			dropped = append(dropped, t)
			continue
		}
		kept = append(kept, t)
	}
	// Só o padrão do tipo (o /S do NSIS, o /qn /norestart do MSI): fica vazio, vale o padrão.
	std := strings.Fields(winget.SilentDefault(typ))
	if typ == "msi" {
		std = append(std, "/qn", "/passive")
	}
	if !slices.ContainsFunc(kept, func(t string) bool {
		return !slices.ContainsFunc(std, func(s string) bool { return strings.EqualFold(s, t) })
	}) {
		return typ, "", dropped
	}
	return typ, strings.Join(kept, " "), dropped
}
