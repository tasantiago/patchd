package biosplain

import "testing"

// O mesmo assunto, com o nome de cada fabricante, vira o mesmo título.
func TestMesmoNomeParaQualquerFabricante(t *testing.T) {
	grupos := map[string][]string{
		"Ligar pela rede":                            {"Wake On LAN", "S5 Wake On LAN", "WakeOnLan", "WakeOnLAN", "Wake on LAN"},
		"Ligar pela rede na bateria":                 {"Wake on LAN on DC mode"},
		"Ligar pela rede quando estiver na dock":     {"WakeOnLANDock"},
		"Ligar pelo Wi-Fi":                           {"Wake on WLAN"},
		"Pedir senha ao ligar pela rede":             {"Wake on LAN Power-on Password Policy"},
		"Economia máxima com o computador desligado": {"S5 Maximum Power Savings", "DeepSleepCtrl", "Deep Sleep Control"},
		"Quando a energia volta depois de uma queda": {"After Power Loss", "AcPwrRcvry", "After Power Loss Recovery"},
		"Iniciar por pendrive ou HD externo":         {"USB Storage Boot", "UsbEmu", "USB Boot", "Boot from USB"},
		"Inicialização rápida":                       {"Fast Boot", "FastBoot", "QuickBoot"},
		"Virtualização":                              {"Virtualization Technology (VTx)", "Virtualization", "VirtualizationTechnology", "SVM CPU Virtualization"},
		"Virtualização de dispositivos":              {"Virtualization Technology for Directed I/O (VTd)", "VtForDirectIo", "VTdFeature"},
		"Inicialização segura (Secure Boot)":         {"Secure Boot", "SecureBoot"},
		"Wi-Fi":                                      {"Wireless Network Device (WLAN)", "WirelessLan", "WirelessLANAccess"},
		"Portas USB":                                 {"Front USB Ports", "USBPortAccess", "UsbPortsExternal"},
		"Placa de rede com cabo":                     {"Embedded LAN Controller", "EmbNic1", "EthernetLANAccess"},
	}
	for titulo, nomes := range grupos {
		for _, n := range nomes {
			if got := Title(n); got != titulo {
				t.Errorf("%q: %q, queria %q", n, got, titulo)
			}
		}
	}
	// Fora do dicionário (ou de propósito fora de uma entrada): o nome do fabricante.
	for _, n := range []string{"UsbEmuNoUsbBoot", "Virtualization Based BIOS Protection", "Secure Boot Keys", "Asset Tracking Number"} {
		if got := Title(n); got != n {
			t.Errorf("%q virou %q", n, got)
		}
	}
}

func TestValoresSimples(t *testing.T) {
	casos := []struct{ nome, valor, quer string }{
		{"Wake On LAN", "Boot to Hard Drive", "Ligado, iniciando o Windows"},
		{"Wake On LAN", "Disable", "Desligado"},
		{"WakeOnLan", "LanOnly", "Ligado, só pelo cabo"},
		{"WakeOnLAN", "ACOnly", "Ligado, só na tomada"},
		{"Wake on LAN", "Primary", "Ligado"},
		{"DeepSleepCtrl", "S4AndS5", "Ligado, desligado ou hibernando"},
		{"After Power Loss", "Previous State", "Voltar como estava"},
		{"After Power Loss", "Power Off", "Ficar desligado"},
		{"AcPwrRcvry", "On", "Ligar"},
		{"AcPwrRcvry", "Last", "Voltar como estava"},
		{"FastBoot", "Thorough", "Completa (verifica tudo, mais lenta)"},
		{"UsbEmu", "Enabled", "Ligado"},
		{"Configure Legacy Support and Secure Boot", "Legacy Support Disable and Secure Boot Enable", "Moderno, com inicialização segura"},
		{"Asset Tracking Number", "ABC123", "ABC123"},
	}
	for _, c := range casos {
		if got := Value(c.nome, c.valor); got != c.quer {
			t.Errorf("%s = %s: %q, queria %q", c.nome, c.valor, got, c.quer)
		}
	}
	if got := Change("UsbEmu", "Disabled"); got != "Iniciar por pendrive ou HD externo: Desligado" {
		t.Errorf("Change: %q", got)
	}
}

// Toda entrada tem título, explicação e um assunto da lista; as chaves não se repetem.
func TestDicionarioCompleto(t *testing.T) {
	seen := map[string]bool{}
	topics := map[string]bool{}
	for _, tp := range Topics {
		topics[tp] = true
	}
	for _, e := range catalog {
		if e.Key == "" || e.Title == "" || e.Help == "" || !topics[e.Topic] || seen[e.Key] {
			t.Errorf("entrada incompleta ou repetida: %+v", e)
		}
		seen[e.Key] = true
	}
}
