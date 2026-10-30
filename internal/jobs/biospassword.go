package jobs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/tasantiago/patchd/internal/biospw"
)

// TypeBIOSPassword (Aula 21.5; desenho em docs/bios-senha.md) grava a senha de administrador
// da BIOS. A senha nova (e a atual, quando a troca a exige) vem fechada para a caixa desta
// máquina (machinebox): o job assinado não leva nenhuma senha legível, nem no banco, nem no
// caminho, nem no log. O agente abre os envelopes só na memória, chama o método do fabricante
// uma vez e responde com o código dele.
const TypeBIOSPassword = "bios_password"

// BIOSPasswordParams são os parâmetros do job bios_password.
type BIOSPasswordParams struct {
	Guard   int64  `json:"guard"`             // a linha da guarda no servidor (bios_passwords.id)
	Vendor  string `json:"vendor"`            // a interface do fabricante: só "hp" por enquanto
	BoxKey  string `json:"box_key"`           // a impressão da chave da caixa que fechou os envelopes
	New     string `json:"new"`               // a senha nova, fechada (machinebox, PurposeBIOS), base64
	Current string `json:"current,omitempty"` // a senha em uso, fechada (PurposeBIOSCurrent); vazio: a BIOS está sem senha
	// Copy: a senha nova fechada para a chave de guarda (biospw.Copy), que o agente deixa no
	// Registro da máquina depois de gravar (docs/bios-senha.md, seção 9). Vazio: sem cópia.
	Copy string `json:"copy,omitempty"`
}

// BIOSVendors: as interfaces que o job aceita. A HP primeiro (é onde o .bat rodou); Dell e
// Lenovo desde docs/bios-config.md, seção 8 (o piloto de cada uma começa por uma máquina).
var BIOSVendors = []string{"hp", "dell", "lenovo"}

// BIOSUncertain: o começo do detalhe do resultado quando o agente não sabe se a BIOS gravou
// (a chamada ao fabricante quebrou no meio). O servidor marca a senha como "incerta".
const BIOSUncertain = "resultado incerto: "

// ErrBIOSUncertain: o executor não sabe se a BIOS gravou. O agente responde "falhou" com o
// detalhe começando por BIOSUncertain.
var ErrBIOSUncertain = errors.New("não se sabe se a BIOS gravou")

var boxKeyIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// maxSealed: um envelope de senha (o KEM do ML-KEM-768+X25519 tem 1120 bytes, mais a senha e
// a etiqueta), em base64, com folga.
const maxSealed = 4096

// ParseBIOSPasswordParams lê e confere os parâmetros.
func ParseBIOSPasswordParams(raw json.RawMessage) (BIOSPasswordParams, error) {
	var p BIOSPasswordParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do bios_password ilegíveis: %w", err)
	}
	vendorOK := false
	for _, v := range BIOSVendors {
		vendorOK = vendorOK || p.Vendor == v
	}
	switch {
	case p.Guard <= 0:
		return p, errors.New("bios_password sem a linha da guarda")
	case !vendorOK:
		return p, fmt.Errorf("bios_password: interface do fabricante %q fora da lista", p.Vendor)
	case !boxKeyIDPattern.MatchString(p.BoxKey):
		return p, errors.New("bios_password: impressão da chave da caixa fora do formato")
	case !sealedOK(p.New):
		return p, errors.New("bios_password: envelope da senha nova inválido")
	case p.Current != "" && !sealedOK(p.Current):
		return p, errors.New("bios_password: envelope da senha atual inválido")
	}
	if p.Copy != "" {
		c, err := biospw.ParseCopy(p.Copy)
		switch {
		case err != nil:
			return p, fmt.Errorf("bios_password: %w", err)
		case c.Guard != p.Guard:
			return p, fmt.Errorf("bios_password: a cópia é da guarda #%d, e o job grava a #%d", c.Guard, p.Guard)
		}
	}
	return p, nil
}

func sealedOK(s string) bool {
	if s == "" || len(s) > maxSealed {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}

// BIOSPasswordParamsJSON monta os parâmetros (já conferidos).
func BIOSPasswordParamsJSON(p BIOSPasswordParams) (json.RawMessage, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, err := ParseBIOSPasswordParams(b); err != nil {
		return nil, err
	}
	return b, nil
}
