// Package machinebox é a "caixa" de cada máquina (Aula 21.3): um par de chaves que o agente
// gera e guarda na pasta de dados, e cuja parte pública ele informa no estado. Com ela, o
// servidor entrega um segredo a UMA máquina (a senha da BIOS, a partir da 21.5) sem que o
// segredo fique legível no banco, nas cópias, no job ou no caminho: só o agente daquela
// máquina abre.
//
// HPKE (RFC 9180) da biblioteca padrão, com o KEM híbrido ML-KEM-768 + X25519 (pós-quântico
// e clássico: quebrar um não basta), HKDF-SHA256 e ChaCha20-Poly1305. Cada uso tem o seu
// rótulo (purpose), que entra no info do HPKE: uma caixa feita para "bios-senha" não abre
// como outra coisa.
package machinebox

import (
	"crypto/hpke"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// FileName: a chave privada, na pasta de dados do agente.
const FileName = "chave-cifragem.key"

const (
	privPrefix = "patchd_mbox_"
	infoPrefix = "patchd/caixa/v1/"
	// MaxPlaintext: segredos pequenos (senhas), não arquivos.
	MaxPlaintext = 4096
)

// PurposeBIOS: a senha da BIOS (Aula 21.4).
const PurposeBIOS = "bios-senha"

// PurposeBIOSCurrent: a senha da BIOS em uso, que a troca exige (Aula 21.5).
const PurposeBIOSCurrent = "bios-senha-atual"

var purposePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

func kem() hpke.KEM   { return hpke.MLKEM768X25519() }
func kdf() hpke.KDF   { return hpke.HKDFSHA256() }
func aead() hpke.AEAD { return hpke.ChaCha20Poly1305() }

// PublicKeySize: o tamanho da chave pública (ML-KEM-768 + X25519).
var PublicKeySize = func() int {
	k, err := kem().GenerateKey()
	if err != nil {
		panic(err)
	}
	return len(k.PublicKey().Bytes())
}()

// Generate cria a chave privada (os bytes, como o HPKE serializa).
func Generate() ([]byte, error) {
	k, err := kem().GenerateKey()
	if err != nil {
		return nil, err
	}
	return k.Bytes()
}

// Public: a chave pública da privada.
func Public(priv []byte) ([]byte, error) {
	k, err := kem().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("chave privada da caixa inválida: %w", err)
	}
	return k.PublicKey().Bytes(), nil
}

// ErrOpen: a caixa não abre (outra máquina, outro rótulo, ou alterada).
var ErrOpen = errors.New("a caixa não abre com esta chave (outra máquina, outro uso ou conteúdo alterado)")

func info(purpose string) ([]byte, error) {
	if !purposePattern.MatchString(purpose) {
		return nil, fmt.Errorf("rótulo de uso inválido %q", purpose)
	}
	return []byte(infoPrefix + purpose), nil
}

// Seal fecha o segredo para a chave pública de uma máquina.
func Seal(pub []byte, purpose string, plaintext []byte) ([]byte, error) {
	in, err := info(purpose)
	if err != nil {
		return nil, err
	}
	if len(plaintext) > MaxPlaintext {
		return nil, fmt.Errorf("segredo com %d bytes (máximo %d)", len(plaintext), MaxPlaintext)
	}
	pk, err := kem().NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("chave pública da máquina inválida: %w", err)
	}
	return hpke.Seal(pk, kdf(), aead(), in, plaintext)
}

// Open abre a caixa com a chave privada desta máquina.
func Open(priv []byte, purpose string, sealed []byte) ([]byte, error) {
	in, err := info(purpose)
	if err != nil {
		return nil, err
	}
	k, err := kem().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("chave privada da caixa inválida: %w", err)
	}
	out, err := hpke.Open(k, kdf(), aead(), in, sealed)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// KeyID: a impressão curta da chave pública (12 hexadecimais do SHA-256), como as outras.
func KeyID(pub []byte) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:6])
}

// EncodePublic e ParsePublic: o formato do estado da máquina (base64).
func EncodePublic(pub []byte) string { return base64.StdEncoding.EncodeToString(pub) }

// ParsePublic lê e confere a chave pública (tamanho e formato do KEM).
func ParsePublic(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != PublicKeySize {
		return nil, errors.New("chave da caixa fora do formato (base64 da chave ML-KEM-768 + X25519)")
	}
	if _, err := kem().NewPublicKey(b); err != nil {
		return nil, fmt.Errorf("chave da caixa inválida: %w", err)
	}
	return b, nil
}

// EncodePrivate e ParsePrivate: o arquivo da chave privada.
func EncodePrivate(priv []byte) string {
	return privPrefix + base64.RawURLEncoding.EncodeToString(priv) + "\n"
}

// ParsePrivate lê o arquivo da chave privada.
func ParsePrivate(s string) ([]byte, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), privPrefix)
	if !ok {
		return nil, errors.New("arquivo da chave da caixa fora do formato")
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return nil, errors.New("arquivo da chave da caixa fora do formato")
	}
	if _, err := Public(b); err != nil {
		return nil, err
	}
	return b, nil
}
