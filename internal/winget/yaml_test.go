package winget

import (
	"reflect"
	"strings"
	"testing"
)

func TestYAMLSubconjunto(t *testing.T) {
	casos := []struct {
		nome, in string
		want     any
	}{
		{"mapa e lista no recuo da chave", "A: 1\nCommands:\n- 7z\n- 7za\nB: x\n",
			map[string]any{"A": "1", "Commands": []any{"7z", "7za"}, "B": "x"}},
		{"lista de mapas", "Installers:\n- Architecture: x64\n  InstallerSwitches:\n    Silent: /S\n- Architecture: x86\n",
			map[string]any{"Installers": []any{
				map[string]any{"Architecture": "x64", "InstallerSwitches": map[string]any{"Silent": "/S"}},
				map[string]any{"Architecture": "x86"}}}},
		{"tudo é texto (26.10 não vira 26.1)", "PackageVersion: 26.10\nInstallerSuccessCodes:\n- 3010\nFlag: true\n",
			map[string]any{"PackageVersion": "26.10", "InstallerSuccessCodes": []any{"3010"}, "Flag": "true"}},
		{"comentários", "# cabeçalho\nInstallerType: exe # InstallShield\n  # solto\nSilent: /S#não é comentário\n",
			map[string]any{"InstallerType": "exe", "Silent": "/S#não é comentário"}},
		{"aspas", `A: "C:\\Temp\\x \"y\""` + "\nB: '%ProgramFiles%/Let''s'\nC: ''\nD: \"\\u00e9\" # fim\n",
			map[string]any{"A": `C:\Temp\x "y"`, "B": "%ProgramFiles%/Let's", "C": "", "D": "é"}},
		{"chave sem valor", "A:\nB: 2\n", map[string]any{"A": "", "B": "2"}},
		{"valor com dois-pontos", "InstallerUrl: https://x.example/a:b\nC: C:\\x\n",
			map[string]any{"InstallerUrl": "https://x.example/a:b", "C": `C:\x`}},
		{"listas e mapas de uma linha", "M: [interactive, 'silent', \"x, y\"]\nS: { Silent: /silent, Log: '/log a' }\nE: []\n",
			map[string]any{"M": []any{"interactive", "silent", "x, y"}, "S": map[string]any{"Silent": "/silent", "Log": "/log a"}, "E": []any{}}},
		{"bloco literal", "ReleaseNotes: |-\n  linha 1\n\n  # não é comentário\nX: 1\n",
			map[string]any{"ReleaseNotes": "linha 1\n\n# não é comentário", "X": "1"}},
		{"bloco dobrado", "ProductCode: >\n  ab\n  cd\n\n  ef\nY: 2\n",
			map[string]any{"ProductCode": "ab cd\nef\n", "Y": "2"}},
		{"BOM, CRLF e ---", "\ufeff---\r\nA: 1\r\nB:\r\n- x\r\n", map[string]any{"A": "1", "B": []any{"x"}}},
		{"item de lista sozinho", "L:\n-\n  K: v\n- w\n", map[string]any{"L": []any{map[string]any{"K": "v"}, "w"}}},
	}
	for _, c := range casos {
		got, err := parseYAML([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.nome, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.nome, got, c.want)
		}
	}
}

// O que foge do subconjunto é recusado, com a linha.
func TestYAMLRecusa(t *testing.T) {
	for nome, c := range map[string]struct{ in, msg string }{
		"âncora":                     {"A:\n- &o0\n  K: v\n", "linha 2: âncoras"},
		"referência":                 {"A: *o0\n", "âncoras"},
		"tag":                        {"A: !!str 1\n", "tags"},
		"valor em mais de uma linha": {"A: abc\n  def\nB: 1\n", "linha 2: recuo inesperado"},
		"aspas em mais de uma linha": {"A: \"abc\n  def\"\n", "aspas sem fechamento"},
		"tabulação":                  {"A:\n\tB: 1\n", "linha 2: tabulação"},
		"dois documentos":            {"A: 1\n---\nB: 2\n", "linha 2: mais de um documento"},
		"chave repetida":             {"A: 1\nA: 2\n", "linha 2: chave \"A\" repetida"},
		"texto solto":                {"A: 1\nsó texto\n", "linha 2: esperava \"chave: valor\""},
		"vazio":                      {"# nada\n", "documento vazio"},
		"bloco com indicador":        {"A: |2\n  x\n", "não é aceito"},
		"lista sem fechar":           {"A: [a, b\n", "sem fechamento"},
		"não UTF-8":                  {"A: \xff\n", "não é UTF-8"},
	} {
		_, err := parseYAML([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: erro %v; esperava %q", nome, err, c.msg)
		}
	}
}
