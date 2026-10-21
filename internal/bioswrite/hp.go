package bioswrite

import "strings"

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
