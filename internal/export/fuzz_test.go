package export

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

// Aula 20.3: os campos da exportação vêm dos agentes (nome da máquina, versão, editor). Para
// qualquer texto, o CSV lido de volta tem as mesmas linhas e colunas, cada célula é o texto
// original (com o apóstrofo, quando começaria como fórmula) e nenhuma célula começa com um
// caractere que a planilha executaria.
func FuzzTable(f *testing.F) {
	f.Add("pc-01", "=HYPERLINK(\"http://x\")", "7-Zip;26.01")
	f.Add("a\"b", "linha\r\nnova", "+1")
	f.Add("@SUM(1)", "\t=1", "-2")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		var out bytes.Buffer
		linhas := [][]string{{a, b, c}, {c, a, b}}
		if err := Table(&out, []string{"x", "y", "z"}, linhas); err != nil {
			t.Fatal(err)
		}
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(out.String(), BOM)))
		r.Comma = ';'
		lido, err := r.ReadAll()
		if err != nil {
			t.Fatalf("o CSV não relê: %v", err)
		}
		if len(lido) != 3 {
			t.Fatalf("%d linhas, esperava 3", len(lido))
		}
		for i, l := range linhas {
			for j, cel := range l {
				got := lido[i+1][j]
				if want := norm(safe(cel)); norm(got) != want {
					t.Fatalf("célula %d,%d: %q, esperava %q", i, j, got, want)
				}
				if got != "" && strings.ContainsRune("=+-@\t\r", rune(got[0])) {
					t.Fatalf("célula começa como fórmula: %q", got)
				}
			}
		}
	})
}

// norm: o encoding/csv não devolve o "\r" de dentro da célula ("\r\n" volta "\n", e um
// "\r" sozinho some). Foi o primeiro achado deste alvo, e era a propriedade que estava
// exigente demais, não o código: a planilha recebe o texto, sem fórmula.
func norm(s string) string { return strings.ReplaceAll(s, "\r", "") }
