// Package release é a atualização do agente (Aula 5.4): o manifesto de uma versão, a
// assinatura Ed25519 que o protege e a pasta de versões que o servidor distribui.
//
// Quem assina é o operador, com a chave privada fora do servidor. O servidor só guarda e
// entrega os arquivos; quem confere a assinatura é o agente, com a chave pública embutida
// no executável. Um servidor invadido, portanto, não consegue fazer a frota rodar um
// executável que o operador não assinou.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PublicKey é a chave pública de atualização, em base64, injetada no build com
//
//	-ldflags "-X github.com/tasantiago/patchd/internal/release.PublicKey=..."
//
// Vazia (builds de desenvolvimento sem PATCHD_UPDATE_PUBKEY): o agente não se atualiza.
var PublicKey = ""

const (
	// ManifestSchemaVersion é a versão do formato do manifesto.
	ManifestSchemaVersion = 1
	// ManifestFile e SignatureFile são os nomes dos arquivos dentro da pasta da versão.
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.sig"
	// MaxManifestSize limita o que o agente aceita baixar como manifesto.
	MaxManifestSize = 1 << 20
	// PrivateKeyPrefix identifica o arquivo da chave privada.
	PrivateKeyPrefix = "patchd_relkey_"
)

// signingContext entra na mensagem assinada antes do manifesto: uma assinatura feita com
// a mesma chave para outra finalidade nunca vale como assinatura de manifesto.
const signingContext = "patchd-agent-manifest-v1\n"

// Manifest descreve uma versão do agente: os executáveis por SO e arquitetura, com o
// SHA-256 e o tamanho de cada um.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Version       string    `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	Files         []File    `json:"files"`
}

// File é um executável da versão.
type File struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"` // hexadecimal
	Size   int64  `json:"size"`
}

// For devolve o executável de um SO e arquitetura.
func (m Manifest) For(goos, goarch string) (File, bool) {
	for _, f := range m.Files {
		if f.OS == goos && f.Arch == goarch {
			return f, true
		}
	}
	return File{}, false
}

var (
	// VersionPattern e NamePattern valem para nomes de pasta e de arquivo: nada de barras,
	// "..", nem nomes começando com ponto.
	VersionPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z.+-]{0,63}$`)
	NamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Check confere a estrutura do manifesto (não a assinatura).
func (m Manifest) Check() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("manifesto com schema_version %d (esperado %d)", m.SchemaVersion, ManifestSchemaVersion)
	}
	if !VersionPattern.MatchString(m.Version) {
		return fmt.Errorf("versão %q inválida no manifesto", m.Version)
	}
	if len(m.Files) == 0 {
		return errors.New("manifesto sem executáveis")
	}
	for _, f := range m.Files {
		if f.OS == "" || f.Arch == "" || !NamePattern.MatchString(f.Name) || !sha256Pattern.MatchString(f.SHA256) || f.Size <= 0 {
			return fmt.Errorf("entrada inválida no manifesto: %+v", f)
		}
	}
	return nil
}

// Sign serializa o manifesto e o assina. Devolve os bytes exatos do manifest.json e o
// conteúdo do manifest.sig (a assinatura em base64).
func Sign(priv ed25519.PrivateKey, m Manifest) (manifest, sig []byte, err error) {
	if err := m.Check(); err != nil {
		return nil, nil, err
	}
	manifest, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	manifest = append(manifest, '\n')
	s := ed25519.Sign(priv, signedMessage(manifest))
	return manifest, []byte(base64.StdEncoding.EncodeToString(s) + "\n"), nil
}

// Verify confere a assinatura sobre os bytes exatos do manifesto e só então o interpreta.
// Nada do conteúdo é usado antes de a assinatura passar.
func Verify(pub ed25519.PublicKey, manifest, sig []byte) (Manifest, error) {
	var m Manifest
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return m, errors.New("assinatura do manifesto ilegível")
	}
	if !ed25519.Verify(pub, signedMessage(manifest), raw) {
		return m, errors.New("assinatura do manifesto inválida (outra chave ou manifesto alterado)")
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifesto assinado, mas ilegível: %w", err)
	}
	return m, m.Check()
}

func signedMessage(manifest []byte) []byte {
	return append([]byte(signingContext), manifest...)
}

// ParsePublicKey lê a chave pública em base64.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("chave pública de atualização inválida (esperado base64 de 32 bytes)")
	}
	return ed25519.PublicKey(raw), nil
}

// EncodePrivateKey e ParsePrivateKey: o arquivo da chave privada guarda a semente de 32
// bytes, em base64 URL, com o prefixo patchd_relkey_.
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return PrivateKeyPrefix + base64.RawURLEncoding.EncodeToString(priv.Seed()) + "\n"
}

func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), PrivateKeyPrefix)
	seed, err := base64.RawURLEncoding.DecodeString(rest)
	if !ok || err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("arquivo de chave privada inválido")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncodePublicKey é o formato do PATCHD_UPDATE_PUBKEY.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// HashBytes é o SHA-256 em hexadecimal, o formato do manifesto.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
