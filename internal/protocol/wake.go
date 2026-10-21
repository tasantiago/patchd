package protocol

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Wake (Aula 22.1) é o que decide se a máquina pode ser ligada pela rede (Wake on LAN), só
// leitura. O agente coleta; o servidor decide se está pronta (internal/wake). Ponteiro nil e
// texto vazio: não se sabe.
type Wake struct {
	// BIOS: o Wake on LAN está ligado no firmware (pela interface do fabricante, Aula 21.1).
	BIOS *bool `json:"bios,omitempty"`
	// DeepSleep: a economia profunda do desligado está ligada (Deep Sleep da Dell, S5 Maximum
	// Power Savings da HP). Ligada, a placa de rede fica sem energia e o WoL não acorda.
	DeepSleep *bool `json:"deep_sleep,omitempty"`
	// Settings: as configurações do firmware que pesaram (nome e valor, como o fabricante
	// escreve), para conferir no laboratório.
	Settings []WakeSetting `json:"settings,omitempty"`
	// Adapters: as placas de rede físicas. nil: não lido; [] nenhuma.
	Adapters []WakeAdapter `json:"adapters,omitempty"`
	// FastStartup: a Inicialização Rápida do Windows (o "desligar" vira hibernação híbrida).
	FastStartup *bool `json:"fast_startup,omitempty"`
	// BitLockerPIN: a unidade do sistema pede PIN na partida (a máquina acordada para na tela
	// do PIN e não atualiza).
	BitLockerPIN *bool `json:"bitlocker_pin,omitempty"`
}

// WakeSetting é uma configuração do firmware.
type WakeSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// WakeAdapter é uma placa de rede física.
type WakeAdapter struct {
	Name  string `json:"name"`
	MAC   string `json:"mac,omitempty"`
	Wired bool   `json:"wired"` // Ethernet (cabo); falso: Wi-Fi ou outra
	// MagicPacket: "Wake on Magic Packet" do driver: WakeOn, WakeOff, WakeUnsupported; vazio:
	// não se sabe.
	MagicPacket string `json:"magic_packet,omitempty"`
	// DeviceWake: "Permitir que este dispositivo ative o computador" (gerenciamento de energia).
	DeviceWake *bool `json:"device_wake,omitempty"`
}

// Valores do WakeAdapter.MagicPacket.
const (
	WakeOn          = "ligado"
	WakeOff         = "desligado"
	WakeUnsupported = "sem-suporte"
)

// Limites do Wake.
const (
	MaxWakeSettings = 16
	MaxWakeAdapters = 16
	maxWakeText     = 128
)

// Check confere tamanhos e valores fechados.
func (w Wake) Check() error {
	if len(w.Settings) > MaxWakeSettings || len(w.Adapters) > MaxWakeAdapters {
		return errors.New("configurações ou placas demais")
	}
	text := func(campo, v string) error {
		if v == "" || len(v) > maxWakeText || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return fmt.Errorf("%s vazio, longo demais ou com caractere de controle", campo)
		}
		return nil
	}
	for _, s := range w.Settings {
		if err := text("nome da configuração", s.Name); err != nil {
			return err
		}
		if s.Value != "" {
			if err := text("valor da configuração", s.Value); err != nil {
				return err
			}
		}
	}
	for _, a := range w.Adapters {
		if err := text("nome da placa", a.Name); err != nil {
			return err
		}
		if a.MAC != "" {
			if _, err := net.ParseMAC(a.MAC); err != nil {
				return fmt.Errorf("placa %q: MAC inválido", a.Name)
			}
		}
		switch a.MagicPacket {
		case "", WakeOn, WakeOff, WakeUnsupported:
		default:
			return fmt.Errorf("placa %q: magic_packet desconhecido %q", a.Name, a.MagicPacket)
		}
	}
	return nil
}
