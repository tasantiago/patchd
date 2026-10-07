package wintrust

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported diz se este sistema confere assinaturas Authenticode.
const Supported = true

// As funções auxiliares do WinVerifyTrust que o x/sys não traz.
// https://learn.microsoft.com/windows/win32/api/wintrust/nf-wintrust-wthelperprovdatafromstatedata
// https://learn.microsoft.com/windows/win32/api/wintrust/nf-wintrust-wthelpergetprovsignerfromchain
var (
	modwintrust        = windows.NewLazySystemDLL("wintrust.dll")
	procProvDataFromSD = modwintrust.NewProc("WTHelperProvDataFromStateData")
	procProvSigner     = modwintrust.NewProc("WTHelperGetProvSignerFromChain")
)

// cryptProviderSgnr é o começo da CRYPT_PROVIDER_SGNR (wintrust.h), até pChainContext.
// O Windows informa o tamanho real em cbStruct; o campo só é lido se couber nele.
type cryptProviderSgnr struct {
	cbStruct          uint32
	verifyAsOf        windows.Filetime
	csCertChain       uint32
	pasCertChain      *cryptProviderCert
	dwSignerType      uint32
	psSigner          uintptr
	dwError           uint32
	csCounterSigners  uint32
	pasCounterSigners uintptr
	pChainContext     *windows.CertChainContext
}

// Verify confere a assinatura Authenticode de path com o WinVerifyTrust e aplica a
// política CheckMicrosoft. A revogação não é consultada (WTD_REVOKE_NONE, só o cache): o
// agente pode estar numa rede sem acesso às listas de revogação, e a busca offline existe
// justamente para esse caso. A Signature volta preenchida sempre que a cadeia foi lida,
// inclusive quando a política recusa (para o log dizer quem assinou).
func Verify(path string) (Signature, error) {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Signature{}, err
	}
	file := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: p16}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
		ProvFlags:                       windows.WTD_CACHE_ONLY_URL_RETRIEVAL,
	}
	verr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	// O estado (e a cadeia apontada por ele) só existe até o CLOSE: tudo é copiado antes.
	defer func() {
		data.StateAction = windows.WTD_STATEACTION_CLOSE
		_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	}()
	if verr != nil {
		return Signature{}, trustError(verr)
	}
	sig, err := signerChain(data.StateData)
	if err != nil {
		return Signature{}, err
	}
	return sig, CheckMicrosoft(sig)
}

// trustError traduz os códigos mais comuns do WinVerifyTrust.
func trustError(err error) error {
	var msg string
	switch {
	case errors.Is(err, windows.Errno(windows.TRUST_E_NOSIGNATURE)):
		msg = "o arquivo não tem assinatura"
	case errors.Is(err, windows.Errno(windows.TRUST_E_SUBJECT_FORM_UNKNOWN)):
		msg = "o formato do arquivo não admite assinatura Authenticode"
	case errors.Is(err, windows.Errno(windows.TRUST_E_BAD_DIGEST)):
		msg = "o conteúdo não confere com a assinatura (arquivo alterado)"
	case errors.Is(err, windows.Errno(windows.CERT_E_UNTRUSTEDROOT)):
		msg = "a cadeia termina numa raiz que este Windows não reconhece"
	case errors.Is(err, windows.Errno(windows.TRUST_E_EXPLICIT_DISTRUST)):
		msg = "o certificado foi marcado como não confiável neste Windows"
	case errors.Is(err, windows.Errno(windows.CERT_E_REVOKED)):
		msg = "o certificado foi revogado"
	default:
		msg = "assinatura recusada"
	}
	var code windows.Errno
	errors.As(err, &code)
	return fmt.Errorf("%s (WinVerifyTrust: 0x%08X)", msg, uint32(code))
}

// signerChain lê a cadeia do primeiro signatário a partir do estado do WinVerifyTrust.
func signerChain(state windows.Handle) (Signature, error) {
	prov, _, _ := procProvDataFromSD.Call(uintptr(state))
	if prov == 0 {
		return Signature{}, errors.New("WTHelperProvDataFromStateData não devolveu os dados da verificação")
	}
	sp, _, _ := procProvSigner.Call(prov, 0, 0, 0)
	if sp == 0 {
		return Signature{}, errors.New("WTHelperGetProvSignerFromChain não devolveu o signatário")
	}
	sgnr := *(**cryptProviderSgnr)(unsafe.Pointer(&sp)) // memória do Windows, válida até o CLOSE
	need := unsafe.Offsetof(sgnr.pChainContext) + unsafe.Sizeof(sgnr.pChainContext)
	if uintptr(sgnr.cbStruct) < need || sgnr.pChainContext == nil {
		return providerCerts(sgnr) // sem a cadeia montada: a lista de certificados do provedor
	}
	chain := sgnr.pChainContext
	if chain.ChainCount == 0 || chain.Chains == nil {
		return Signature{}, errors.New("cadeia de certificados vazia")
	}
	simple := unsafe.Slice(chain.Chains, chain.ChainCount)[0]
	if simple == nil || simple.NumElements == 0 || simple.Elements == nil {
		return Signature{}, errors.New("cadeia de certificados vazia")
	}
	var sig Signature
	for _, e := range unsafe.Slice(simple.Elements, simple.NumElements) {
		if e == nil || e.CertContext == nil {
			return Signature{}, errors.New("certificado ausente na cadeia")
		}
		sig.Chain = append(sig.Chain, certInfo(e.CertContext))
	}
	return sig, nil
}

// cryptProviderCert é o começo da CRYPT_PROVIDER_CERT (wintrust.h).
type cryptProviderCert struct {
	cbStruct uint32
	pCert    *windows.CertContext
}

// providerCerts lê pasCertChain, a cadeia na forma antiga (do que assinou até a raiz). Os
// itens têm o tamanho informado em cbStruct pelo próprio Windows.
func providerCerts(sgnr *cryptProviderSgnr) (Signature, error) {
	if sgnr.csCertChain == 0 || sgnr.pasCertChain == nil {
		return Signature{}, errors.New("o signatário não traz a cadeia de certificados")
	}
	base := unsafe.Pointer(sgnr.pasCertChain)
	stride := uintptr(sgnr.pasCertChain.cbStruct)
	if stride < unsafe.Sizeof(cryptProviderCert{}) {
		return Signature{}, errors.New("cadeia de certificados ilegível")
	}
	var sig Signature
	for i := uintptr(0); i < uintptr(sgnr.csCertChain); i++ {
		c := (*cryptProviderCert)(unsafe.Add(base, i*stride))
		if c.pCert == nil {
			return Signature{}, errors.New("certificado ausente na cadeia")
		}
		sig.Chain = append(sig.Chain, certInfo(c.pCert))
	}
	return sig, nil
}

// OIDs do nome (X.520), na forma que o CertGetNameString espera: texto ANSI terminado em 0.
var (
	oidCommonName   = []byte("2.5.4.3\x00")
	oidOrganization = []byte("2.5.4.10\x00")
)

func certInfo(c *windows.CertContext) Cert {
	der := unsafe.Slice(c.EncodedCert, c.Length)
	sum := sha1.Sum(der) // impressão digital: identifica o certificado, não é uma conferência
	return Cert{
		CommonName:   nameAttr(c, oidCommonName),
		Organization: nameAttr(c, oidOrganization),
		SHA1:         hex.EncodeToString(sum[:]),
	}
}

// nameAttr lê um atributo do nome do titular.
// https://learn.microsoft.com/windows/win32/api/wincrypt/nf-wincrypt-certgetnamestringw
func nameAttr(c *windows.CertContext, oid []byte) string {
	n := windows.CertGetNameString(c, windows.CERT_NAME_ATTR_TYPE, 0, unsafe.Pointer(&oid[0]), nil, 0)
	if n <= 1 {
		return ""
	}
	buf := make([]uint16, n)
	windows.CertGetNameString(c, windows.CERT_NAME_ATTR_TYPE, 0, unsafe.Pointer(&oid[0]), &buf[0], n)
	return windows.UTF16ToString(buf)
}
