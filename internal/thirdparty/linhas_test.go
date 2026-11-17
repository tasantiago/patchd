package thirdparty

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// As linhas com regra própria (instalação pelo pacote do winget da linha) e o que fica na
// regra geral: a outra linha, o de 32 bits e o agente que não informa a arquitetura.
func TestLinhasComRegraPropria(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		sw    protocol.Software
		regra string
	}{
		{protocol.Software{Name: "Node.js", Version: "22.21.0", Publisher: "Node.js Foundation", Arch: "x64"}, "nodejs-22"},
		{protocol.Software{Name: "Node.js", Version: "22.21.0", Publisher: "Node.js Foundation", Arch: "x86"}, "nodejs"},
		{protocol.Software{Name: "Node.js", Version: "22.21.0", Publisher: "Node.js Foundation"}, "nodejs"},
		{protocol.Software{Name: "Node.js", Version: "20.19.0", Publisher: "Node.js Foundation", Arch: "x64"}, "nodejs"},
		{protocol.Software{Name: "Node.js", Version: "122.0.0", Publisher: "Node.js Foundation", Arch: "x64"}, "nodejs"},
		{protocol.Software{Name: "Eclipse Temurin JDK with Hotspot 11.0.28+6 (x64)", Version: "11.0.28.6", Publisher: "Eclipse Adoptium"}, "temurin-11-jdk"},
		{protocol.Software{Name: "Eclipse Temurin JRE with Hotspot 21.0.4+7 (x64)", Version: "21.0.4.7", Publisher: "Eclipse Adoptium"}, "temurin-21-jre"},
		{protocol.Software{Name: "Eclipse Temurin JRE with Hotspot 21+35 (x64)", Version: "21.0.0.35", Publisher: "Eclipse Adoptium"}, "temurin-21-jre"},
		{protocol.Software{Name: "Eclipse Temurin JRE with Hotspot 8u402-b06 (x64)", Version: "8.0.402.6", Publisher: "Eclipse Adoptium"}, "temurin-8-jre"},
		{protocol.Software{Name: "Eclipse Temurin JRE with Hotspot 8u402-b06 (x86)", Version: "8.0.402.6", Publisher: "Eclipse Adoptium"}, "eclipse-temurin"},
		{protocol.Software{Name: "Eclipse Temurin JDK with Hotspot 110.0.1+1 (x64)", Version: "110.0.1.1", Publisher: "Eclipse Adoptium"}, "eclipse-temurin"},
		{protocol.Software{Name: "Python 3.13.9 (64-bit)", Version: "3.13.9150.0", Publisher: "Python Software Foundation"}, "python-3.13"},
		{protocol.Software{Name: "Python 3.12.3 (32-bit)", Version: "3.12.3150.0", Publisher: "Python Software Foundation"}, "python-3.12"},
		{protocol.Software{Name: "Python 3.11.4 (64-bit)", Version: "3.11.4150.0", Publisher: "Python Software Foundation"}, "python-3.11"},
		{protocol.Software{Name: "Python 3.10.6 (64-bit)", Version: "3.10.6150.0", Publisher: "Python Software Foundation"}, "python"},
		{protocol.Software{Name: "Python 3.131.0 (64-bit)", Version: "3.131.0", Publisher: "Python Software Foundation"}, "python"},
	}
	for _, c := range casos {
		c.sw.Scope = "machine"
		r := Evaluate("windows", []protocol.Software{c.sw}, m, nil)
		if len(r.Findings) != 1 || r.Findings[0].App.ID != c.regra {
			got := "nenhuma"
			if len(r.Findings) == 1 {
				got = r.Findings[0].App.ID
			}
			t.Errorf("%s %s (%s): regra %s; esperava %s", c.sw.Name, c.sw.Version, c.sw.Arch, got, c.regra)
		}
	}
	// version_match e arch inválidos não passam na conferência.
	for _, extra := range []string{`, "version_match": "("`, `, "arch": "ia64"`} {
		if _, err := Parse([]byte(`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "minimum", "match": "x", "min_version": "1"` + extra + `}]}`)); err == nil {
			t.Errorf("%s aceito", extra)
		}
	}
	if _, err := Parse([]byte(`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "minimum", "match": "x", "min_version": "1",
		"install": {"winget": "A.B", "type": "wix", "signer": "A", "arguments": "/i outro.msi"}}]}`)); err == nil {
		t.Error("install.arguments fora da lista aceito")
	}
}
