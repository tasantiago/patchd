package selfservice

import "testing"

// Aula 17.2: o grupo da oferta contra os grupos do token (DOMINIO\grupo).
func TestTokenMember(t *testing.T) {
	m := TokenMember([]string{`EXEMPLO\Domain Users`, `EXEMPLO\GG-Exemplo`, `BUILTIN\Users`})
	for g, want := range map[string]bool{
		"GG-Exemplo": true, "gg-exemplo": true, `EXEMPLO\GG-Exemplo`: true, `OUTRO\GG-Exemplo`: true,
		"CN=GG-Exemplo,OU=Grupos,DC=exemplo,DC=local": true, // oferta antiga, da 17.1
		"GG-Outro": false, "Exemplo": false, "": false,
	} {
		if m(g) != want {
			t.Errorf("%q: %v", g, !want)
		}
	}
}
