package panel

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Parâmetros do hash das senhas: PBKDF2-HMAC-SHA256 com 600.000 iterações, o mínimo
// recomendado pela OWASP (Password Storage Cheat Sheet). A biblioteca padrão tem o
// PBKDF2 desde o Go 1.24 (crypto/pbkdf2), com implementação validada no FIPS 140-3.
const (
	pbkdf2Iterations = 600_000
	saltLen          = 16
	keyLen           = 32
	hashScheme       = "pbkdf2-sha256"

	// Limites aceitos ao ler um hash do banco: abaixo, um hash adulterado faria a
	// conferência de graça; acima, faria cada login levar minutos.
	minIterations = 100_000
	maxIterations = 10_000_000
)

// Limites da senha. O mínimo segue a OWASP para senhas sem segundo fator; o máximo
// só evita corpos absurdos (o PBKDF2 não fica mais lento com senhas longas).
const (
	MinPasswordChars = 12
	MaxPasswordBytes = 1024
)

// CheckPasswordPolicy confere uma senha nova.
func CheckPasswordPolicy(pw string) error {
	switch {
	case !utf8.ValidString(pw):
		return fmt.Errorf("a senha não é UTF-8 válido")
	case utf8.RuneCountInString(pw) < MinPasswordChars:
		return fmt.Errorf("a senha precisa de pelo menos %d caracteres", MinPasswordChars)
	case len(pw) > MaxPasswordBytes:
		return fmt.Errorf("a senha passa de %d bytes", MaxPasswordBytes)
	case strings.TrimSpace(pw) != pw:
		return fmt.Errorf("a senha começa ou termina com espaço (provável erro de cópia)")
	}
	return nil
}

// HashPassword devolve "pbkdf2-sha256$<iterações>$<sal>$<hash>", com sal aleatório.
// O formato carrega os parâmetros: quando as iterações subirem, os hashes antigos
// continuam conferindo.
func HashPassword(pw string) (string, error) {
	return hashPassword(pw, pbkdf2Iterations)
}

func hashPassword(pw string, iter int) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, iter, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, iter, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword confere a senha contra o hash guardado, em tempo constante na
// comparação. Hash fora do formato devolve false.
func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < minIterations || iter > maxIterations {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(salt) < saltLen || len(want) != keyLen {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, keyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash é conferido quando o usuário não existe: o login leva o mesmo tempo nos dois
// casos, e o tempo de resposta não revela quais nomes existem.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("senha-ficticia-so-para-gastar-o-mesmo-tempo")
	if err != nil {
		panic(fmt.Sprintf("crypto/rand falhou: %v", err))
	}
	return h
})

// CheckPasswordUnknownUser gasta o tempo de uma conferência e devolve false.
func CheckPasswordUnknownUser(pw string) bool {
	CheckPassword(dummyHash(), pw)
	return false
}
