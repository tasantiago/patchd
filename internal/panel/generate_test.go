package panel

import (
	"strings"
	"testing"
)

// GeneratePassword (Aula 15.3): 20 símbolos do alfabeto sem ambíguos, na política, diferentes.
func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := GeneratePassword()
		if err != nil || len(pw) != GeneratedPasswordChars || CheckPasswordPolicy(pw) != nil || seen[pw] {
			t.Fatalf("%q %v", pw, err)
		}
		if strings.ContainsAny(pw, "0O1lI") || strings.Trim(pw, passwordAlphabet) != "" {
			t.Fatalf("fora do alfabeto: %q", pw)
		}
		seen[pw] = true
	}
}
