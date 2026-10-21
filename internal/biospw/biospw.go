// Package biospw sorteia a senha de administrador da BIOS de cada máquina e a fecha para a
// guarda (Aula 21.4). O desenho está em docs/bios-senha.md.
//
// A guarda é um par HPKE (o mesmo de internal/machinebox, com outro propósito): a pública
// fecha, a privada abre. Decisão D1 da 21.3: a privada fica no servidor, num arquivo de
// segredo, e o painel revela a senha a um admin, com motivo, auditoria e aviso.
package biospw

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/machinebox"
)

// Decisão D3: 12 caracteres em todos os fabricantes (os ThinkPads mais antigos não aceitam
// mais). O alfabeto: na tela da BIOS o teclado é sempre o americano, e no ABNT2 os símbolos
// mudam de lugar; então só minúsculas e números, sem os ambíguos (0/o, 1/l/i). São 31
// símbolos: 12 deles dão cerca de 59 bits.
const (
	Length   = 12
	Alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
)

// PurposeEscrow: o propósito do envelope da guarda. Amarrado na derivação da chave: um
// envelope da guarda não abre como envelope de outra coisa (e o contrário).
const PurposeEscrow = "bios-guarda"

// Generate sorteia uma senha. Rejeição: byte >= 248 (8 x 31) é descartado, para cada símbolo
// ter a mesma chance (o resto da divisão de 256 por 31 favoreceria os primeiros).
func Generate() (string, error) {
	const limit = 256 - 256%len(Alphabet)
	out := make([]byte, 0, Length)
	buf := make([]byte, 32)
	for len(out) < Length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < Length {
				out = append(out, Alphabet[int(b)%len(Alphabet)])
			}
		}
	}
	return string(out), nil
}

// ErrFormat: a senha não tem o tamanho ou o alfabeto da guarda.
var ErrFormat = fmt.Errorf("a senha da guarda tem %d caracteres do alfabeto %q", Length, Alphabet)

// Check confere o formato.
func Check(pw string) error {
	if len(pw) != Length || strings.Trim(pw, Alphabet) != "" {
		return ErrFormat
	}
	return nil
}

// Grouped: a senha em grupos de 4, para ler na tela ("abcd efgh jkmn"). Digita-se sem os espaços.
func Grouped(pw string) string {
	var b strings.Builder
	for i, r := range pw {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ErrOtherMachine: o envelope é de outra máquina (alguém trocou envelopes no banco).
var ErrOtherMachine = errors.New("o envelope da guarda é de outra máquina")

// Seal fecha a senha para a chave pública da guarda. O ID da máquina vai junto, dentro do
// envelope: trocar envelopes entre máquinas no banco não passa despercebido na abertura.
func Seal(escrowPub []byte, machineID, pw string) ([]byte, error) {
	if err := Check(pw); err != nil {
		return nil, err
	}
	if machineID == "" || strings.ContainsAny(machineID, "\n\x00") {
		return nil, errors.New("ID de máquina inválido")
	}
	return machinebox.Seal(escrowPub, PurposeEscrow, []byte(machineID+"\n"+pw))
}

// Open abre o envelope com a chave privada da guarda e confere a máquina e o formato.
func Open(escrowPriv []byte, machineID string, sealed []byte) (string, error) {
	pt, err := machinebox.Open(escrowPriv, PurposeEscrow, sealed)
	if err != nil {
		return "", err
	}
	id, pw, ok := strings.Cut(string(pt), "\n")
	switch {
	case !ok:
		return "", ErrFormat
	case id != machineID:
		return "", ErrOtherMachine
	}
	if err := Check(pw); err != nil {
		return "", err
	}
	return pw, nil
}
