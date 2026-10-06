package apple

import (
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

// appleRootPEM é a "Apple Root CA" (Apple Inc., válida até 2035), a raiz da cadeia do
// gdmf.apple.com: gdmf.apple.com → Apple Server Authentication CA → Apple Root CA. Ela não
// está no repositório de certificados do sistema, e por isso o curl sem configuração falha
// com "self-signed certificate in certificate chain" (Aula 6.2, parte 1).
// Fonte: https://www.apple.com/certificateauthority/ ("Apple Inc. Root").
//
//go:embed apple_root_ca.pem
var appleRootPEM []byte

// AppleRootSHA256 é a impressão digital SHA-256 do certificado, a mesma publicada pela
// Apple e conferida no laboratório. Se o arquivo embutido mudar, o pool não é montado.
const AppleRootSHA256 = "b0b1730ecbc7ff4505142c49f1295e6eda6bcaed7e2c68c5be91b5a11001f024"

// AppleRootPool devolve um pool só com a raiz da Apple, depois de conferir a impressão
// digital. Usado apenas no cliente do gdmf: o resto do patchd segue com as raízes do sistema.
func AppleRootPool() (*x509.CertPool, error) {
	block, rest := pem.Decode(appleRootPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("raiz da Apple embutida ilegível")
	}
	if b, _ := pem.Decode(rest); b != nil {
		return nil, fmt.Errorf("o arquivo da raiz da Apple tem mais de um certificado")
	}
	sum := sha256.Sum256(block.Bytes)
	if got := hex.EncodeToString(sum[:]); got != AppleRootSHA256 {
		return nil, fmt.Errorf("impressão digital da raiz da Apple não confere: %s", got)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("raiz da Apple embutida: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, nil
}
