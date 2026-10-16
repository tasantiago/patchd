package selfservice

import (
	"strings"
)

// TokenMember (Aula 17.2): o teste de grupo pelo token do Windows. A oferta guarda o
// sAMAccountName (com ou sem DOMINIO\); o token traz DOMINIO\grupo. Compara o nome do grupo,
// sem o domínio e sem diferenciar maiúsculas. Uma oferta antiga com DN (17.1) compara pelo CN.
func TokenMember(tokenGroups []string) func(string) bool {
	have := map[string]bool{}
	for _, g := range tokenGroups {
		have[strings.ToLower(groupName(g))] = true
	}
	return func(offer string) bool { return have[strings.ToLower(groupName(offer))] }
}

// groupName: "DOMINIO\GG-X" → "GG-X"; "CN=GG-X,OU=...,DC=..." → "GG-X"; "GG-X" → "GG-X".
func groupName(g string) string {
	g = strings.TrimSpace(g)
	if i := strings.LastIndex(g, `\`); i >= 0 {
		return g[i+1:]
	}
	if strings.HasPrefix(strings.ToUpper(g), "CN=") {
		g = g[3:]
		if i := strings.Index(g, ","); i >= 0 {
			g = g[:i]
		}
	}
	return g
}
