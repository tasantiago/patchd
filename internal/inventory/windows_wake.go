package inventory

import (
	"net"
	"sort"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
)

// Ligar pela rede no Windows (Aula 22.1), só leitura. Como o firmware da 21.1, fica fora do
// arquivo _windows.go para ser testável em qualquer sistema: a coleta (wake_windows.go)
// preenche wmiWake, e esta parte decide o que cada valor significa.
//
// De onde vem cada coisa:
//   - firmware, pela interface do fabricante: HP (HP_BIOSEnumeration: Name, CurrentValue),
//     Dell (root\dcim\sysman\biosattributes, EnumerationAttribute: AttributeName,
//     CurrentValue) e Lenovo (Lenovo_BiosSetting: CurrentSetting "Nome,Valor");
//   - placas: MSFT_NetAdapter (física, cabo), MSFT_NetAdapterPowerManagementSettingData
//     (WakeOnMagicPacket) e MSPower_DeviceWakeEnable (a caixa "Permitir que este dispositivo
//     ative o computador");
//   - Fast Startup: HiberbootEnabled no registro;
//   - BitLocker com PIN: Win32_EncryptableVolume.GetKeyProtectors da unidade do sistema.
//
// Os nomes das configurações mudam de modelo para modelo. A coleta guarda as que parecem de
// WoL ou de economia profunda (com o nome e o valor como vieram), e a interpretação só decide
// com valores conhecidos; o resto fica "não se sabe".

// wmiWake são os valores lidos.
type wmiWake struct {
	Vendor       string           // a interface do fabricante (protocol.FirmwareHP...)
	BIOSRows     []map[string]any // as configurações do firmware, como vieram
	Adapters     []map[string]any // MSFT_NetAdapter
	Power        []map[string]any // MSFT_NetAdapterPowerManagementSettingData
	DeviceWake   []map[string]any // MSPower_DeviceWakeEnable
	FastStartup  *bool
	BitLockerPIN *bool
}

// norm: o nome sem espaços, hífens, barras e sublinhados, em minúsculas ("S5 Wake-on-LAN" →
// "s5wakeonlan").
func norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '/', '\t':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// wakeRelevant: a configuração é de WoL ou de economia profunda.
func wakeRelevant(name string) bool {
	n := norm(name)
	for _, k := range []string{"wakeonlan", "wakeonwlan", "deepsleep", "maximumpowersaving", "maxpowersaving"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

// biosSettings: as configurações do firmware que importam, em nome e valor.
func biosSettings(vendor string, rows []map[string]any) []protocol.WakeSetting {
	var out []protocol.WakeSetting
	add := func(name, value string) {
		name, value = texto.Clip(strings.TrimSpace(name), 120), texto.Clip(strings.TrimSpace(value), 120)
		if name != "" && wakeRelevant(name) && len(out) < protocol.MaxWakeSettings {
			out = append(out, protocol.WakeSetting{Name: name, Value: value})
		}
	}
	for _, r := range rows {
		switch vendor {
		case protocol.FirmwareHP:
			add(asString(r["Name"]), asString(r["CurrentValue"]))
		case protocol.FirmwareDell:
			add(asString(r["AttributeName"]), firstValue(r["CurrentValue"]))
		case protocol.FirmwareLenovo:
			// "WakeOnLAN,ACOnly;[Optional:Disable,ACOnly,ACandBattery,Enable]"
			setting, _, _ := strings.Cut(asString(r["CurrentSetting"]), ";")
			name, value, _ := strings.Cut(setting, ",")
			add(name, value)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// firstValue: o Dell devolve o valor atual como lista de um item.
func firstValue(v any) string {
	if l, ok := v.([]any); ok {
		if len(l) == 0 {
			return ""
		}
		return asString(l[0])
	}
	return asString(v)
}

// Valores conhecidos (normalizados) que ligam ou desligam.
var (
	wakeOffValues = map[string]bool{"disable": true, "disabled": true, "off": true}
	wakeOnValues  = map[string]bool{"enable": true, "enabled": true, "on": true, "lanonly": true, "lanwlan": true,
		"aconly": true, "acandbattery": true, "boottonetwork": true, "boottoharddrive": true, "followbootorder": true,
		"primary": true, "automatic": true}
	deepOnValues = map[string]bool{"enable": true, "enabled": true, "s5only": true, "s4ands5": true, "s4s5": true}
)

// wakeFirmware: WoL no firmware e economia profunda, pelas configurações lidas. Um WoL
// ligado basta (alguns modelos têm um para cada estado); economia profunda ligada em qualquer
// uma pesa contra.
func wakeFirmware(settings []protocol.WakeSetting) (bios, deep *bool) {
	for _, s := range settings {
		n, v := norm(s.Name), norm(s.Value)
		switch {
		case strings.Contains(n, "wakeonlan") && !strings.Contains(n, "wlan") && !strings.Contains(n, "dock"):
			switch {
			case wakeOnValues[v]:
				bios = boolPtr(true)
			case wakeOffValues[v] && bios == nil:
				bios = boolPtr(false)
			}
		case strings.Contains(n, "deepsleep"), strings.Contains(n, "maximumpowersaving"), strings.Contains(n, "maxpowersaving"):
			switch {
			case deepOnValues[v]:
				deep = boolPtr(true)
			case wakeOffValues[v] && deep == nil:
				deep = boolPtr(false)
			}
		}
	}
	return bios, deep
}

// Os valores de MSFT_NetAdapter e MSFT_NetAdapterPowerManagementSettingData.
const (
	ndisMedium8023 = 14 // NdisPhysicalMedium802_3: Ethernet
	pmUnsupported  = 0  // WakeOnMagicPacket: sem suporte
	pmDisabled     = 1
	pmEnabled      = 2
)

// wakeAdapters junta as três consultas por placa física.
func wakeAdapters(nets, power, devWake []map[string]any) []protocol.WakeAdapter {
	magic := map[string]string{}
	for _, p := range power {
		v, ok := asUint64(p["WakeOnMagicPacket"])
		switch {
		case !ok:
		case v == pmEnabled:
			magic[asString(p["Name"])] = protocol.WakeOn
		case v == pmDisabled:
			magic[asString(p["Name"])] = protocol.WakeOff
		case v == pmUnsupported:
			magic[asString(p["Name"])] = protocol.WakeUnsupported
		}
	}
	out := []protocol.WakeAdapter{}
	for _, n := range nets {
		if hw, ok := n["HardwareInterface"].(bool); ok && !hw {
			continue
		}
		if virt, ok := n["Virtual"].(bool); ok && virt {
			continue
		}
		name := texto.Clip(asString(n["Name"]), 120)
		if name == "" || len(out) == protocol.MaxWakeAdapters {
			continue
		}
		a := protocol.WakeAdapter{Name: name, MagicPacket: magic[name]}
		if m, err := net.ParseMAC(strings.ReplaceAll(asString(n["MacAddress"]), "-", ":")); err == nil {
			a.MAC = m.String()
		}
		if medium, ok := asUint64(n["NdisPhysicalMedium"]); ok {
			a.Wired = medium == ndisMedium8023
		}
		// MSPower_DeviceWakeEnable.InstanceName = PnPDeviceID + "_0".
		pnp := strings.ToUpper(asString(n["PnPDeviceID"]))
		for _, d := range devWake {
			inst := strings.ToUpper(asString(d["InstanceName"]))
			if pnp != "" && strings.HasPrefix(inst, pnp+"_") {
				if on, ok := d["Enable"].(bool); ok {
					a.DeviceWake = boolPtr(on)
				}
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// windowsWakeInfo monta o Wake; nil quando nada foi lido.
func windowsWakeInfo(w wmiWake) *protocol.Wake {
	var out protocol.Wake
	out.Settings = biosSettings(w.Vendor, w.BIOSRows)
	out.BIOS, out.DeepSleep = wakeFirmware(out.Settings)
	if w.Adapters != nil {
		out.Adapters = wakeAdapters(w.Adapters, w.Power, w.DeviceWake)
	}
	out.FastStartup, out.BitLockerPIN = w.FastStartup, w.BitLockerPIN
	if out.BIOS == nil && out.DeepSleep == nil && out.Settings == nil && out.Adapters == nil && out.FastStartup == nil && out.BitLockerPIN == nil {
		return nil
	}
	return &out
}
