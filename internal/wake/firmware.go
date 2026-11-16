package wake

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O Wake on LAN e a economia profunda no firmware, pelas configurações que o agente leu da
// BIOS (nome e valor, como o fabricante escreve). O agente decide com esta regra; o servidor
// decide de novo quando o agente não soube (agente antigo, que não conhecia um valor): o
// cartão da máquina não espera a atualização do agente.

// Valores conhecidos (normalizados) que ligam ou desligam.
var (
	offValues = map[string]bool{"disable": true, "disabled": true, "off": true}
	onValues  = map[string]bool{"enable": true, "enabled": true, "on": true, "lanonly": true, "lanwlan": true,
		"aconly": true, "acandbattery": true, "boottonetwork": true, "boottoharddrive": true, "followbootorder": true,
		"bootorder": true, // Lenovo ThinkCentre: acorda e segue a ordem de boot normal
		"primary":   true, "automatic": true}
	deepOnValues = map[string]bool{"enable": true, "enabled": true, "s5only": true, "s4ands5": true, "s4s5": true}
)

// normSetting: sem espaços, hífens, barras e sublinhados, em minúsculas ("S5 Wake-on-LAN" →
// "s5wakeonlan").
func normSetting(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '/', '\t':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// Firmware: o WoL ligado no firmware e a economia profunda ligada; nil: não se sabe. Um WoL
// ligado basta (alguns modelos têm um para cada estado); economia profunda ligada em qualquer
// uma pesa contra. Valor desconhecido não decide nada.
func Firmware(settings []protocol.WakeSetting) (bios, deep *bool) {
	yes, no := true, false
	for _, s := range settings {
		n, v := normSetting(s.Name), normSetting(s.Value)
		switch {
		case strings.Contains(n, "wakeonlan") && !strings.Contains(n, "wlan") && !strings.Contains(n, "dock"):
			switch {
			case onValues[v]:
				bios = &yes
			case offValues[v] && bios == nil:
				bios = &no
			}
		case strings.Contains(n, "deepsleep"), strings.Contains(n, "maximumpowersaving"), strings.Contains(n, "maxpowersaving"):
			switch {
			case deepOnValues[v]:
				deep = &yes
			case offValues[v] && deep == nil:
				deep = &no
			}
		}
	}
	return bios, deep
}
