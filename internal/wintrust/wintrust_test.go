package wintrust

import (
	"strings"
	"testing"
)

func TestCheckMicrosoft(t *testing.T) {
	raiz := Cert{CommonName: "Microsoft Root Certificate Authority 2011", Organization: "Microsoft Corporation"}
	pca := Cert{CommonName: "Microsoft Code Signing PCA 2011", Organization: "Microsoft Corporation"}
	casos := []struct {
		nome string
		sig  Signature
		erro string // vazio: aceita
	}{
		{"microsoft", Signature{Chain: []Cert{{CommonName: "Microsoft Corporation", Organization: "Microsoft Corporation"}, pca, raiz}}, ""},
		{"microsoft windows", Signature{Chain: []Cert{{CommonName: "Microsoft Windows", Organization: "Microsoft Corporation"}, pca, raiz}}, ""},
		{"outra empresa", Signature{Chain: []Cert{{CommonName: "Google LLC", Organization: "Google LLC"},
			{CommonName: "DigiCert Trusted G4 Code Signing RSA4096 SHA384 2021 CA1"}, {CommonName: "DigiCert Trusted Root G4"}}}, "não pela Microsoft"},
		{"nome copiado, raiz de outra AC", Signature{Chain: []Cert{{CommonName: "Microsoft Corporation", Organization: "Microsoft Corporation"},
			{CommonName: "Alguma AC"}, {CommonName: "Alguma Raiz"}}}, "não numa raiz da Microsoft"},
		{"organização parecida", Signature{Chain: []Cert{{CommonName: "Microsoft Corporation", Organization: "Microsoft Corporation Ltda"}, pca, raiz}}, "não pela Microsoft"},
		{"sem cadeia", Signature{}, "sem cadeia"},
	}
	for _, c := range casos {
		err := CheckMicrosoft(c.sig)
		switch {
		case c.erro == "" && err != nil:
			t.Errorf("%s: recusada: %v", c.nome, err)
		case c.erro != "" && (err == nil || !strings.Contains(err.Error(), c.erro)):
			t.Errorf("%s: erro = %v, esperado contendo %q", c.nome, err, c.erro)
		}
	}
}

func TestSignatureString(t *testing.T) {
	s := Signature{Chain: []Cert{{CommonName: "A"}, {CommonName: "B"}, {CommonName: "C"}}}
	if got := s.String(); got != "A <- B <- C" {
		t.Errorf("String() = %q", got)
	}
	if s.Signer().CommonName != "A" || s.Root().CommonName != "C" {
		t.Errorf("Signer/Root = %v/%v", s.Signer(), s.Root())
	}
}
