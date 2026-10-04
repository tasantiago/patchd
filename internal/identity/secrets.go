// Package identity reúne o que define quem é uma máquina para o patchd: os segredos
// (token de enrollment e credencial da máquina), o ID emitido pelo servidor e, na
// segunda parte da Aula 4.3, a comparação das evidências de identidade.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Prefixos dos segredos. Dizem o tipo do segredo à primeira vista (num log vazado, num
// script esquecido) e deixam o servidor recusar um segredo do tipo errado sem consultar o banco.
const (
	EnrollmentPrefix = "patchd_enr_" // token de enrollment: criado pelo administrador, usado na instalação
	CredentialPrefix = "patchd_mac_" // credencial da máquina: emitida no enrollment, usada em todo envio
)

// NewSecret gera um segredo com 256 bits aleatórios e devolve o texto (entregue uma vez)
// e o hash (o único que é guardado).
func NewSecret(prefix string) (plain string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand falhou: %v", err)) // sem aleatoriedade, nenhum segredo é seguro
	}
	plain = prefix + base64.RawURLEncoding.EncodeToString(b)
	return plain, Hash(plain)
}

// Hash é o SHA-256 do segredo. Um hash simples basta porque o segredo tem 256 bits
// aleatórios: não há dicionário nem força bruta possível. Senhas escolhidas por pessoas,
// ao contrário, exigiriam um hash lento (bcrypt, argon2).
func Hash(plain string) []byte {
	s := sha256.Sum256([]byte(plain))
	return s[:]
}

// NewMachineID gera o ID da máquina: um UUID versão 4 (aleatório). Ele não deriva de
// nenhuma evidência: se derivasse do hostname ou do serial, herdaria os defeitos deles.
func NewMachineID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand falhou: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40 // versão 4
	b[8] = b[8]&0x3f | 0x80 // variante RFC 9562
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ErrEnrollmentRejected: token de enrollment desconhecido, expirado, esgotado ou revogado.
// O motivo vai no erro (para o log do servidor); o cliente recebe só "recusado".
var ErrEnrollmentRejected = errors.New("enrollment recusado")

// BearerToken extrai o segredo do cabeçalho "Authorization: Bearer <segredo>".
// O esquema não diferencia maiúsculas (RFC 9110); o segredo, sim.
func BearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
