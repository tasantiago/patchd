package biosstd

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/protocol"
)

func enum(name, value string, possible ...string) protocol.BIOSSetting {
	return protocol.BIOSSetting{Name: name, Value: value, Kind: protocol.BIOSKindEnum, Possible: possible}
}

func mudancas(f Finding) string {
	var out []string
	for _, c := range f.Changes {
		out = append(out, c.Name+"="+c.Value)
	}
	return strings.Join(out, "; ")
}

// O padrão resolve o mesmo objetivo com os nomes e os valores de cada fabricante, só com o que
// a máquina informou.
func TestPadraoPorFabricante(t *testing.T) {
	hpWake := []string{"Disable", "Boot to Network", "Boot to Hard Drive", "Boot to Normal Boot Order"}
	dellWake := []string{"Disabled", "LanOnly", "WlanOnly", "LanWlan", "LanWithPxeBoot"}
	lenovoWake := []string{"Disable", "ACOnly", "ACandBattery", "Enable"}
	casos := []struct {
		nome     string
		goal     string
		settings []protocol.BIOSSetting
		state    string
		quer     string // as mudanças, ou um pedaço da nota
	}{
		{"HP desligado", GoalWake, []protocol.BIOSSetting{enum("Wake On LAN", "Disable", hpWake...)}, StateFix, "Wake On LAN=Boot to Hard Drive"},
		{"HP ligado para a rede: fica como está", GoalWake, []protocol.BIOSSetting{enum("Wake On LAN", "Boot to Network", hpWake...)}, StateOK, ""},
		{"HP com economia profunda", GoalWake, []protocol.BIOSSetting{enum("Wake On LAN", "Boot to Hard Drive", hpWake...),
			enum("S5 Maximum Power Savings", "Enable", "Disable", "Enable")}, StateFix, "S5 Maximum Power Savings=Disable"},
		{"HP antigo, um por estado", GoalWake, []protocol.BIOSSetting{enum("S5 Wake On LAN", "Disable", "Disable", "Enable"),
			enum("Wake on WLAN", "Disable", "Disable", "Enable"), enum("Wake on LAN on DC mode", "Disable", "Disable", "Enable")},
			StateFix, "S5 Wake On LAN=Enable; Wake on LAN on DC mode=Enable"},
		{"HP: a política de senha do WoL não conta", GoalWake, []protocol.BIOSSetting{enum("Wake on LAN Power-on Password Policy", "Bypass Password", "Bypass Password", "Require Password")},
			StateMissing, "não informa uma configuração de Wake on LAN"},
		{"Dell desligado, com Deep Sleep", GoalWake, []protocol.BIOSSetting{enum("WakeOnLan", "Disabled", dellWake...), enum("DeepSleepCtrl", "S4AndS5", "Disabled", "S5", "S4AndS5")},
			StateFix, "WakeOnLan=LanOnly; DeepSleepCtrl=Disabled"},
		{"Dell só Wi-Fi: liga o cabo sem tirar o Wi-Fi", GoalWake, []protocol.BIOSSetting{enum("WakeOnLan", "WlanOnly", dellWake...)}, StateFix, "WakeOnLan=LanWlan"},
		{"Lenovo notebook", GoalWake, []protocol.BIOSSetting{enum("WakeOnLAN", "Disable", lenovoWake...), enum("WakeOnLANDock", "Disable", "Disable", "Enable")},
			StateFix, "WakeOnLAN=ACOnly"},
		{"Lenovo desktop", GoalWake, []protocol.BIOSSetting{enum("Wake on LAN", "Disabled", "Primary", "Automatic", "Disabled")}, StateFix, "Wake on LAN=Primary"},
		{"só leitura", GoalWake, []protocol.BIOSSetting{{Name: "Wake On LAN", Value: "Disable", Kind: protocol.BIOSKindEnum, Possible: hpWake, ReadOnly: true}},
			StateStuck, "só leitura"},
		{"sem valor que ligue", GoalWake, []protocol.BIOSSetting{enum("Wake On LAN", "Disable", "Disable", "Talvez")}, StateStuck, "não tem, entre os valores possíveis"},
		{"modelo sem a configuração", GoalWake, []protocol.BIOSSetting{enum("Fast Boot", "Enable", "Disable", "Enable")}, StateMissing, "não informa"},

		{"HP boot por USB", GoalUSBBoot, []protocol.BIOSSetting{enum("USB Storage Boot", "Enable", "Disable", "Enable")}, StateFix, "USB Storage Boot=Disable"},
		{"HP já desligado", GoalUSBBoot, []protocol.BIOSSetting{enum("USB Storage Boot", "Disable", "Disable", "Enable")}, StateOK, ""},
		{"Dell", GoalUSBBoot, []protocol.BIOSSetting{enum("UsbEmu", "Enabled", "Disabled", "Enabled")}, StateFix, "UsbEmu=Disabled"},
		{"Dell antigo: o sentido contrário fica de fora", GoalUSBBoot, []protocol.BIOSSetting{enum("UsbEmuNoUsbBoot", "Disabled", "Disabled", "Enabled")},
			StateMissing, "não informa"},
		{"um modelo com 'USB Boot'", GoalUSBBoot, []protocol.BIOSSetting{enum("USB Boot", "Enabled", "Enabled", "Disabled")}, StateFix, "USB Boot=Disabled"},
		{"Lenovo: o suporte legado a USB não é boot", GoalUSBBoot, []protocol.BIOSSetting{enum("USBBIOSSupport", "Enable", "Disable", "Enable"),
			enum("USBPortAccess", "Enable", "Disable", "Enable")}, StateMissing, "não informa"},
		{"a ordem de boot não é escolha", GoalUSBBoot, []protocol.BIOSSetting{{Name: "USB Boot Order", Value: "Enabled", Kind: protocol.BIOSKindList}}, StateMissing, "não informa"},
	}
	for _, c := range casos {
		f := Evaluate(c.goal, c.settings)
		got := mudancas(f)
		if f.State != StateFix {
			got = f.Note
		}
		if f.State != c.state || !strings.Contains(got, c.quer) || (c.state == StateFix && got != c.quer) {
			t.Errorf("%s: estado %q, %q (queria %q, %q)", c.nome, f.State, got, c.state, c.quer)
		}
		// Tudo o que o padrão propõe passa pela lista fechada, com os valores da máquina.
		for _, ch := range f.Changes {
			i := slices.IndexFunc(c.settings, func(s protocol.BIOSSetting) bool { return s.Name == ch.Name })
			if i < 0 || biosconfig.CheckChange(c.settings[i], ch) != nil {
				t.Errorf("%s: a mudança %+v não passa na lista fechada", c.nome, ch)
			}
		}
		if f.State != StateFix && len(f.Changes) != 0 {
			t.Errorf("%s: mudanças fora do estado 'corrigir'", c.nome)
		}
	}
}

// O filtro do banco (NamePattern sobre o nome normalizado) traz tudo o que o padrão avalia.
func TestNamePatternCobreOPadrao(t *testing.T) {
	re := regexp.MustCompile(NamePattern)
	for _, nome := range []string{"Wake On LAN", "S5 Wake On LAN", "WakeOnLan", "DeepSleepCtrl", "S5 Maximum Power Savings", "USB Storage Boot", "UsbEmu",
		"USB Boot", "Boot from USB"} {
		if !re.MatchString(biosconfig.Norm(nome)) {
			t.Errorf("%q fica fora do filtro", nome)
		}
	}
	for _, nome := range []string{"Fast Boot", "UsbEmulation", "Virtualization Technology (VTx)"} {
		if re.MatchString(biosconfig.Norm(nome)) {
			t.Errorf("%q entra no filtro sem precisar", nome)
		}
	}
	for _, r := range NormStrip {
		if biosconfig.Norm("a"+string(r)+"b") != "ab" {
			t.Errorf("NormStrip tem %q, que o Norm não tira", r)
		}
	}
}
