package winget

import (
	"os"
	"path/filepath"
	"testing"
)

// Aula 20.3: o manifesto do winget vem do GitHub (T5, fonte externa) e passa pelo nosso
// leitor de YAML, escrito à mão para o subconjunto que o winget usa. Para qualquer entrada,
// nada de pânico; e o que o leitor aceita, ele lê igual de novo (sem estado escondido).
func FuzzParseInstaller(f *testing.F) {
	arquivos, _ := filepath.Glob("testdata/*.yaml")
	for _, a := range arquivos {
		if b, err := os.ReadFile(a); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("A: \"\\u00e9\\x41\"\nB: [a, 'b''c']\nC:\n- D: 1\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, err := parseYAML(b); err != nil {
			return
		}
		_, _ = ParseInstaller(b)
	})
}
