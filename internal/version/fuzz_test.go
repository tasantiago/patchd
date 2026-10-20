package version

import "testing"

// Aula 20.3: as versões vêm dos agentes (inventário) e das fontes (catálogo), e decidem se
// uma máquina está em dia. Para quaisquer duas versões que um esquema aceita, a comparação
// tem de ser uma ordem: a = a, e trocar os lados troca o sinal. Uma comparação que não é
// ordem dá respostas diferentes conforme a ordem da lista, e o compliance oscila.
func FuzzCompare(f *testing.F) {
	f.Add("1:2.38-1ubuntu6.3", "2.38-1ubuntu6.10")
	f.Add("1.0~rc1", "1.0")
	f.Add("6.17.4-200.fc44", "6.17.10-200.fc44")
	f.Add("1.0^git1", "1.0")
	f.Add("26200.7019", "26200.10000")
	f.Add("v1.2.3-rc.1+b", "1.2.3")
	f.Fuzz(func(t *testing.T, a, b string) {
		for nome, cmp := range map[string]func(string, string) (int, error){
			"dpkg": CompareDpkg, "rpm": CompareRPM, "semver": CompareSemver, "dotted": CompareDotted,
		} {
			ab, err1 := cmp(a, b)
			ba, err2 := cmp(b, a)
			if err1 != nil || err2 != nil {
				continue
			}
			if ab != -ba {
				t.Fatalf("%s: %q vs %q = %d, mas %q vs %q = %d", nome, a, b, ab, b, a, ba)
			}
			if aa, err := cmp(a, a); err == nil && aa != 0 {
				t.Fatalf("%s: %q vs ele mesmo = %d", nome, a, aa)
			}
		}
	})
}
