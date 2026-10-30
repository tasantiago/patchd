package biospw

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/machinebox"
)

// A cópia de emergência (docs/bios-senha.md, seção 9): o envelope da guarda numa linha de
// texto, para viver fora do servidor — no arquivo do `patchd-server bios export` e no
// Registro da própria máquina. Sem a chave privada de guarda ela não revela nada: é o mesmo
// envelope que o banco guarda, preso ao ID da máquina.
//
//	patchd-bios-1:<máquina>:<guarda>:<impressão da chave>:<envelope em base64>

// CopyPrefix abre toda cópia (e muda se o formato mudar).
const CopyPrefix = "patchd-bios-1"

// maxCopy: uma cópia de verdade tem uns 200 caracteres.
const maxCopy = 2048

// Copy é uma senha guardada, fechada para a chave de guarda.
type Copy struct {
	MachineID string
	Guard     int64  // a linha da guarda (bios_passwords.id)
	KeyID     string // a impressão da chave de guarda que fechou
	Escrow    []byte // o envelope (Seal)
}

var (
	copyIDPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$`)
	copyKeyPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
	// ErrCopy: o texto não é uma cópia de emergência.
	ErrCopy = errors.New("cópia da senha da BIOS fora do formato (" + CopyPrefix + ":máquina:guarda:chave:envelope)")
	// ErrOtherKey: a cópia foi fechada com outra chave de guarda.
	ErrOtherKey = errors.New("a cópia foi fechada com outra chave de guarda")
)

func (c Copy) check() error {
	if !copyIDPattern.MatchString(c.MachineID) || c.Guard < 1 || !copyKeyPattern.MatchString(c.KeyID) || len(c.Escrow) == 0 {
		return ErrCopy
	}
	return nil
}

// Format escreve a cópia numa linha.
func (c Copy) Format() (string, error) {
	if err := c.check(); err != nil {
		return "", err
	}
	return strings.Join([]string{CopyPrefix, c.MachineID, strconv.FormatInt(c.Guard, 10), c.KeyID,
		base64.StdEncoding.EncodeToString(c.Escrow)}, ":"), nil
}

// ParseCopy lê uma cópia (espaços e quebras de linha nas pontas são ignorados: ela costuma
// chegar colada do terminal).
func ParseCopy(s string) (Copy, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxCopy {
		return Copy{}, ErrCopy
	}
	parts := strings.Split(s, ":")
	if len(parts) != 5 || parts[0] != CopyPrefix {
		return Copy{}, ErrCopy
	}
	guard, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Copy{}, ErrCopy
	}
	env, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return Copy{}, ErrCopy
	}
	c := Copy{MachineID: parts[1], Guard: guard, KeyID: parts[3], Escrow: env}
	if err := c.check(); err != nil {
		return Copy{}, err
	}
	return c, nil
}

// Reveal abre a cópia com a chave privada de guarda.
func (c Copy) Reveal(escrowPriv []byte) (string, error) {
	pub, err := machinebox.Public(escrowPriv)
	if err != nil {
		return "", err
	}
	if id := machinebox.KeyID(pub); id != c.KeyID {
		return "", fmt.Errorf("%w (%s; esta chave é %s)", ErrOtherKey, c.KeyID, id)
	}
	return Open(escrowPriv, c.MachineID, c.Escrow)
}
