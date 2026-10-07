// Package wintrust confere assinaturas Authenticode (Aula 6.6). O agente do Windows só usa
// o wsusscn2.cab recebido do servidor depois que o próprio Windows (WinVerifyTrust) confirma
// que o arquivo está íntegro e foi assinado pela Microsoft: um servidor comprometido não
// consegue entregar um catálogo falso, porque a chave que assina está com a Microsoft, não
// com o servidor.
//
// https://learn.microsoft.com/windows/win32/api/wintrust/nf-wintrust-winverifytrust
// https://learn.microsoft.com/windows/win32/wua_sdk/using-wua-to-scan-for-updates-offline
package wintrust

import (
	"errors"
	"fmt"
	"strings"
)

// Cert é o que interessa de um certificado da cadeia.
type Cert struct {
	CommonName   string // CN
	Organization string // O
	SHA1         string // impressão digital, como o certmgr mostra
}

// Signature é a assinatura conferida: a cadeia do certificado que assinou até a raiz.
type Signature struct {
	Chain []Cert // [0] assinou; o último é a raiz confiável
}

// Signer é o certificado que assinou.
func (s Signature) Signer() Cert {
	if len(s.Chain) == 0 {
		return Cert{}
	}
	return s.Chain[0]
}

// Root é a raiz da cadeia.
func (s Signature) Root() Cert {
	if len(s.Chain) == 0 {
		return Cert{}
	}
	return s.Chain[len(s.Chain)-1]
}

// String resume a assinatura numa linha: "CN (O) <- ... <- raiz".
func (s Signature) String() string {
	parts := make([]string, len(s.Chain))
	for i, c := range s.Chain {
		parts[i] = c.CommonName
	}
	return strings.Join(parts, " <- ")
}

// ErrUnsupported: fora do Windows não há WinVerifyTrust.
var ErrUnsupported = errors.New("a conferência de assinatura Authenticode só existe no Windows")

const (
	microsoftOrganization = "Microsoft Corporation"
	microsoftRootPrefix   = "Microsoft Root Certificate Authority" // 2010, 2011...
)

// CheckMicrosoft é a política: o certificado que assinou é da Microsoft Corporation, e a
// cadeia termina numa raiz da Microsoft. O WinVerifyTrust já garantiu que o conteúdo bate
// com a assinatura, que o certificado serve para assinar código e que a raiz está no
// repositório de raízes confiáveis do Windows; sem esta política, qualquer programa
// assinado (um instalador do Chrome, por exemplo) passaria.
func CheckMicrosoft(s Signature) error {
	if len(s.Chain) == 0 {
		return errors.New("assinatura sem cadeia de certificados")
	}
	if signer := s.Signer(); signer.Organization != microsoftOrganization {
		return fmt.Errorf("assinado por %q (O=%q), não pela Microsoft", signer.CommonName, signer.Organization)
	}
	if root := s.Root(); !strings.HasPrefix(root.CommonName, microsoftRootPrefix) {
		return fmt.Errorf("a cadeia termina em %q, não numa raiz da Microsoft", root.CommonName)
	}
	return nil
}
