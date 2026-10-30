package bioswrite

import (
	"fmt"
	"math"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// A interface da HP (HP Client Management Interface, namespace root\HP\InstrumentedBIOS):
//   - a senha de administrador é a configuração "Setup Password", trocada pelo método
//     SetBIOSSetting(Name, Value, Password) da classe HP_BIOSSettingInterface;
//   - as senhas vão com o prefixo <utf-16/> (texto em UTF-16, como o WMI entrega); sem senha
//     em uso, a atual vai só com o prefixo;
//   - o HP Sure Admin aparece como a configuração "Enhanced BIOS Authentication Mode" de
//     HP_BIOSEnumeration; "Enable" = ligado. Modelo sem ela = sem Sure Admin.
const hpPrefix = "<utf-16/>"

// hpPassword: o valor como a HP espera.
func hpPassword(pw []byte) string { return hpPrefix + string(pw) }

// hpSureAdminOn: o valor atual da configuração liga o Sure Admin.
func hpSureAdminOn(current any) bool {
	s, _ := current.(string)
	return strings.EqualFold(strings.TrimSpace(s), "Enable")
}

// vendorOf: a interface pelo fabricante do SMBIOS (o mesmo critério do inventário, Aula 21.1).
func vendorOf(manufacturer string) string {
	m := strings.ToLower(strings.TrimSpace(manufacturer))
	if strings.HasPrefix(m, "hp") || strings.Contains(m, "hewlett") {
		return "hp"
	}
	return ""
}

// hpEnumSettings converte as linhas de HP_BIOSEnumeration (Name, Path, CurrentValue,
// PossibleValues, IsReadOnly) nas configurações de escolha, como o agente as confere antes de
// gravar. Nomes repetidos ficam com a primeira linha.
func hpEnumSettings(rows []map[string]any) []protocol.BIOSSetting {
	var out []protocol.BIOSSetting
	seen := map[string]bool{}
	for _, r := range rows {
		name, _ := r["Name"].(string)
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		s := protocol.BIOSSetting{Name: name, Kind: protocol.BIOSKindEnum}
		s.Path, _ = r["Path"].(string)
		cur, _ := r["CurrentValue"].(string)
		s.Value = strings.TrimSpace(cur)
		if l, ok := r["PossibleValues"].([]any); ok {
			for _, v := range l {
				if pv, ok := v.(string); ok && strings.TrimSpace(pv) != "" {
					s.Possible = append(s.Possible, strings.TrimSpace(pv))
				}
			}
		}
		if ro, err := asCode(r["IsReadOnly"]); err == nil {
			s.ReadOnly = ro != 0
		}
		out = append(out, s)
	}
	return out
}

// asCode: o Return do WMI vem como inteiro de algum tamanho.
func asCode(v any) (uint32, error) {
	var n int64
	switch x := v.(type) {
	case int32:
		n = int64(x)
	case uint32:
		n = int64(x)
	case int64:
		n = x
	case uint64:
		if x > math.MaxUint32 {
			return 0, fmt.Errorf("código de retorno fora da faixa: %d", x)
		}
		n = int64(x)
	case int16:
		n = int64(x)
	case uint16:
		n = int64(x)
	case uint8:
		n = int64(x)
	default:
		return 0, fmt.Errorf("código de retorno de tipo inesperado %T", v)
	}
	if n < 0 || n > math.MaxUint32 {
		return 0, fmt.Errorf("código de retorno fora da faixa: %d", n)
	}
	return uint32(n), nil
}
