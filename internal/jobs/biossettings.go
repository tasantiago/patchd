package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/protocol"
)

// TypeBIOSSettings (docs/bios-config.md) altera configurações da BIOS da lista fechada
// (internal/biosconfig). A senha de administrador em uso, quando a BIOS tem, vem fechada para
// a caixa desta máquina com o rótulo machinebox.PurposeBIOSConfig. O agente confere de novo a
// lista fechada e os valores possíveis da hora, suspende o BitLocker por um reinício e grava
// uma configuração por chamada, parando no primeiro "senha não confere".
const TypeBIOSSettings = "bios_settings"

// BIOSSettingsParams são os parâmetros do job bios_settings.
type BIOSSettingsParams struct {
	Vendor   string              `json:"vendor"`             // a interface do fabricante: só "hp" por enquanto
	BoxKey   string              `json:"box_key"`            // a impressão da chave da caixa que fechou o envelope
	Password string              `json:"password,omitempty"` // a senha em uso, fechada; vazio: a BIOS está sem senha
	Changes  []biosconfig.Change `json:"changes"`
}

// ParseBIOSSettingsParams lê e confere os parâmetros: cada alteração tem de estar na lista
// fechada pelo nome e pelo valor (os valores possíveis da máquina o agente confere na hora).
func ParseBIOSSettingsParams(raw json.RawMessage) (BIOSSettingsParams, error) {
	var p BIOSSettingsParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do bios_settings ilegíveis: %w", err)
	}
	switch {
	case !slices.Contains(BIOSVendors, p.Vendor):
		return p, fmt.Errorf("bios_settings: interface do fabricante %q fora da lista", p.Vendor)
	case !boxKeyIDPattern.MatchString(p.BoxKey):
		return p, errors.New("bios_settings: impressão da chave da caixa fora do formato")
	case p.Password != "" && !sealedOK(p.Password):
		return p, errors.New("bios_settings: envelope da senha inválido")
	case len(p.Changes) == 0 || len(p.Changes) > biosconfig.MaxChanges:
		return p, fmt.Errorf("bios_settings: de 1 a %d alterações", biosconfig.MaxChanges)
	}
	seen := map[string]bool{}
	for _, c := range p.Changes {
		bad := func(v string, max int) bool {
			return v == "" || len(v) > max || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f })
		}
		rule, ok := biosconfig.Lookup(c.Name)
		switch {
		case bad(c.Name, protocol.MaxBIOSName) || bad(c.Value, protocol.MaxBIOSValue):
			return p, errors.New("bios_settings: nome ou valor vazio, longo demais ou com caractere de controle")
		case seen[c.Name]:
			return p, fmt.Errorf("bios_settings: %q repetida", c.Name)
		case !ok:
			return p, fmt.Errorf("bios_settings: %q fora da lista fechada", c.Name)
		case !rule.ValueAllowed(c.Value):
			return p, fmt.Errorf("bios_settings: valor %q recusado para %q", c.Value, c.Name)
		}
		seen[c.Name] = true
	}
	return p, nil
}

// BIOSSettingsParamsJSON monta os parâmetros (já conferidos).
func BIOSSettingsParamsJSON(p BIOSSettingsParams) (json.RawMessage, error) {
	b, err := json.Marshal(p) // #nosec G117 -- Password é o envelope fechado para a caixa da máquina, não a senha
	if err != nil {
		return nil, err
	}
	if _, err := ParseBIOSSettingsParams(b); err != nil {
		return nil, err
	}
	return b, nil
}
