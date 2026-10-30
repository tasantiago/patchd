package inventory

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
)

// O firmware no Windows (Aula 21.1), só leitura. Fica fora do arquivo _windows.go para ser
// testável em qualquer sistema: a coleta (hardware_windows.go) preenche wmiFirmware e esta
// parte decide o que cada valor significa.
//
// As senhas vêm da interface WMI de cada fabricante, que diz só se a senha está definida:
//   - HP: root\HP\InstrumentedBIOS, HP_BIOSPassword (Name, IsSet)
//   - Dell: root\dcim\sysman\wmisecurity, PasswordObject (NameId, IsPasswordSet)
//   - Lenovo: root\WMI, Lenovo_BiosPasswordSettings (PasswordState, um campo de bits)
//   - HP Sure Admin (Aula 21.5): HP_BIOSEnumeration, "Enhanced BIOS Authentication Mode"
// Modelo sem a interface (antigo, ou sem o pacote do fabricante): as senhas ficam sem
// informação, sem erro.

// wmiFirmware são os valores lidos para o firmware.
type wmiFirmware struct {
	FirmwareType uint32 // GetFirmwareType: 1 BIOS legado, 2 UEFI; 0 não se sabe
	SecureBoot   *bool  // HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State UEFISecureBootEnabled
	TPMQueried   bool   // a consulta ao Win32_Tpm rodou (sem linha: sem TPM)
	TPM          map[string]any
	Interface    string           // a interface do fabricante que respondeu
	Passwords    []map[string]any // HP_BIOSPassword ou PasswordObject (Dell)
	LenovoState  map[string]any   // Lenovo_BiosPasswordSettings
	// Aula 21.5: o HP Sure Admin (HP_BIOSEnumeration "Enhanced BIOS Authentication Mode").
	// SureAdminQueried: a consulta rodou; SureAdmin nil: o modelo não tem a configuração.
	SureAdminQueried bool
	SureAdmin        map[string]any
	// docs/bios-config.md: as configurações da BIOS da HP, como vieram de cada classe.
	SettingsQueried bool
	Settings        []hpSettingRow
	// docs/bios-config.md, seção 8: as da Dell e da Lenovo, já traduzidas (internal/biosvendor).
	VendorSettings []protocol.BIOSSetting
}

// hpSettingRow é uma linha de uma das classes de configuração da HP.
type hpSettingRow struct {
	Kind string // protocol.BIOSKind*
	Row  map[string]any
}

// hpBIOSSettings monta as configurações: cortadas nos limites do protocolo, sem nomes
// repetidos, em ordem estável (o hash do inventário não muda à toa) e, por garantia, sem nada
// que pareça senha.
func hpBIOSSettings(rows []hpSettingRow) []protocol.BIOSSetting {
	out := []protocol.BIOSSetting{}
	seen := map[string]bool{}
	for _, r := range rows {
		name := texto.Clip(strings.TrimSpace(asString(r.Row["Name"])), protocol.MaxBIOSName)
		if name == "" || seen[name] || strings.Contains(strings.ToLower(name), "password") || hasControl(name) {
			continue
		}
		s := protocol.BIOSSetting{Name: name, Kind: r.Kind, Path: clipValue(asString(r.Row["Path"]))}
		ro, _ := asUint64(r.Row["IsReadOnly"])
		s.ReadOnly = ro != 0
		switch r.Kind {
		case protocol.BIOSKindEnum:
			s.Value = clipValue(asString(r.Row["CurrentValue"]))
			if l, ok := r.Row["PossibleValues"].([]any); ok {
				for _, v := range l {
					if pv := clipValue(asString(v)); pv != "" && len(s.Possible) < protocol.MaxBIOSPossible && !slices.Contains(s.Possible, pv) {
						s.Possible = append(s.Possible, pv)
					}
				}
			}
		case protocol.BIOSKindInteger:
			if n, ok := asUint64(r.Row["IntegerValue"]); ok {
				s.Value = strconv.FormatUint(n, 10)
			}
		default:
			s.Value = clipValue(asString(r.Row["Value"]))
		}
		seen[name] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Path != out[k].Path {
			return out[i].Path < out[k].Path
		}
		return out[i].Name < out[k].Name
	})
	if len(out) > protocol.MaxBIOSSettings {
		out = out[:protocol.MaxBIOSSettings]
	}
	return out
}

// clipValue: o valor cortado no limite, sem caracteres de controle (o protocolo os recusa e
// derrubaria o firmware inteiro do relatório).
func clipValue(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(v))
	return texto.Clip(strings.TrimSpace(v), protocol.MaxBIOSValue)
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// firmwareInterface: qual interface consultar, pelo fabricante do SMBIOS.
func firmwareInterface(manufacturer string) string {
	m := strings.ToLower(manufacturer)
	switch {
	case strings.HasPrefix(m, "hp") || strings.Contains(m, "hewlett"):
		return protocol.FirmwareHP
	case strings.HasPrefix(m, "dell"):
		return protocol.FirmwareDell
	case strings.HasPrefix(m, "lenovo"):
		return protocol.FirmwareLenovo
	}
	return ""
}

func boolPtr(b bool) *bool { return &b }

// windowsFirmwareInfo monta o Firmware; nil quando nada foi lido.
func windowsFirmwareInfo(w wmiFirmware) *protocol.Firmware {
	var f protocol.Firmware
	switch w.FirmwareType {
	case 1:
		f.Mode = protocol.FirmwareLegacy
		f.SecureBoot = boolPtr(false) // sem UEFI, não há Secure Boot
	case 2:
		f.Mode = protocol.FirmwareUEFI
		f.SecureBoot = w.SecureBoot
	}
	if w.TPMQueried {
		f.TPM = tpmVersion(w.TPM)
	}
	switch w.Interface {
	case protocol.FirmwareHP:
		for _, p := range w.Passwords {
			name := strings.ToLower(asString(p["Name"]))
			set, ok := asUint64(p["IsSet"])
			switch {
			case !ok:
			case strings.Contains(name, "setup"):
				f.AdminPassword = boolPtr(set != 0)
			case strings.Contains(name, "power"):
				f.PowerOnPassword = boolPtr(set != 0)
			}
		}
	case protocol.FirmwareDell:
		for _, p := range w.Passwords {
			set, ok := asUint64(p["IsPasswordSet"])
			switch name := strings.ToLower(asString(p["NameId"])); {
			case !ok:
			case name == "admin":
				f.AdminPassword = boolPtr(set != 0)
			case name == "system":
				f.PowerOnPassword = boolPtr(set != 0)
			}
		}
	case protocol.FirmwareLenovo:
		// PasswordState: bit 0 senha de ligar, bit 1 senha de supervisor (os outros bits
		// são das senhas de disco e de gerenciamento).
		if st, ok := asUint64(w.LenovoState["PasswordState"]); ok {
			f.PowerOnPassword, f.AdminPassword = boolPtr(st&1 != 0), boolPtr(st&2 != 0)
		}
	}
	if f.AdminPassword != nil || f.PowerOnPassword != nil {
		f.Interface = w.Interface
		if f.Interface == protocol.FirmwareHP && w.SureAdminQueried {
			v, _ := w.SureAdmin["CurrentValue"].(string)
			f.SureAdmin = boolPtr(strings.EqualFold(strings.TrimSpace(v), "Enable")) // sem a configuração: desligado
		}
		switch {
		case !w.SettingsQueried:
		case f.Interface == protocol.FirmwareHP:
			f.Settings = hpBIOSSettings(w.Settings)
		default:
			f.Settings = append([]protocol.BIOSSetting{}, w.VendorSettings...)
		}
	}
	if f.Empty() {
		return nil
	}
	return &f
}

// tpmVersion: a versão da especificação do Win32_Tpm ("2.0, 0, 1.59" → "2.0"); sem linha:
// sem TPM.
func tpmVersion(tpm map[string]any) string {
	if tpm == nil {
		return protocol.TPMNone
	}
	v, _, _ := strings.Cut(asString(tpm["SpecVersion"]), ",")
	switch v = strings.TrimSpace(v); v {
	case "2.0", "1.2":
		return v
	}
	return ""
}
