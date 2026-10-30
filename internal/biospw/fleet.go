package biospw

import (
	"errors"
	"strings"

	"github.com/tasantiago/patchd/internal/machinebox"
)

// A senha padrão da frota (docs/bios-senha.md, seção 10): a senha que um fabricante tem em
// todas as máquinas antes da primeira troca pela guarda (posta à mão ou pelo .bat). O servidor a
// guarda fechada para a chave de guarda, presa ao fabricante, e a usa como senha atual na
// primeira troca de cada máquina, no lugar de o admin digitá-la máquina por máquina.

// PurposeFleet: o rótulo do envelope (diferente do da guarda: um não abre como o outro).
const PurposeFleet = "bios-frota"

// MaxFleet: o maior tamanho aceito (o mesmo da senha atual digitada no painel).
const MaxFleet = 64

// ErrFleetFormat: a senha padrão está fora do que o fabricante aceita pela interface.
var ErrFleetFormat = errors.New("senha padrão inválida")

// CheckFleet confere a senha para o fabricante. A Lenovo recebe a senha numa linha separada
// por vírgula e ponto e vírgula, como teclado americano: só ASCII visível, sem os separadores.
func CheckFleet(vendor, pw string) error {
	switch {
	case pw == "" || len(pw) > MaxFleet:
		return errors.New("a senha padrão tem de 1 a 64 caracteres")
	case strings.ContainsFunc(pw, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return errors.New("a senha padrão não pode ter caractere de controle")
	case vendor == "lenovo" && strings.ContainsFunc(pw, func(r rune) bool { return r < 0x21 || r > 0x7e }):
		return errors.New("na Lenovo, a senha vai como teclado americano: só letras sem acento, números e símbolos, sem espaço")
	case vendor == "lenovo" && strings.ContainsAny(pw, ",;"):
		return errors.New("na Lenovo, a senha não pode ter vírgula nem ponto e vírgula (a interface os usa como separador)")
	}
	return nil
}

// SealFleet fecha a senha padrão do fabricante para a chave de guarda.
func SealFleet(escrowPub []byte, vendor, pw string) ([]byte, error) {
	if err := CheckFleet(vendor, pw); err != nil {
		return nil, err
	}
	if vendor == "" || strings.ContainsAny(vendor, "\n\x00") {
		return nil, errors.New("fabricante inválido")
	}
	return machinebox.Seal(escrowPub, PurposeFleet, []byte(vendor+"\n"+pw))
}

// ErrOtherVendor: o envelope é da senha padrão de outro fabricante.
var ErrOtherVendor = errors.New("o envelope é da senha padrão de outro fabricante")

// OpenFleet abre a senha padrão.
func OpenFleet(escrowPriv []byte, vendor string, sealed []byte) (string, error) {
	pt, err := machinebox.Open(escrowPriv, PurposeFleet, sealed)
	if err != nil {
		return "", err
	}
	defer clear(pt)
	v, pw, ok := strings.Cut(string(pt), "\n")
	switch {
	case !ok:
		return "", ErrFleetFormat
	case v != vendor:
		return "", ErrOtherVendor
	}
	return pw, nil
}
