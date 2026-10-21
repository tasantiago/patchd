// Package wake decide se uma máquina pode ser ligada pela rede (Wake on LAN), a partir do que
// o agente coletou (protocol.Wake, Aula 22.1). O agente coleta, o servidor decide: a mesma
// regra serve o cartão da máquina e o resumo da frota (patchd-server inventory summary).
package wake

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Estados.
const (
	Ready   = "pronta"
	Blocked = "bloqueada"
	Unknown = "sem informação"
)

// Motivos que impedem (Blockers) e avisos que não impedem, mas pedem atenção (Notes). Textos
// fixos: o resumo da frota conta por eles.
const (
	NoWired       = "sem placa de rede com cabo"
	BIOSOff       = "Wake on LAN desligado na BIOS"
	DeepSleepOn   = "economia profunda ligada na BIOS (a placa fica sem energia no desligado)"
	MagicOff      = "placa sem Wake on Magic Packet"
	DeviceWakeOff = "placa sem permissão para ligar o computador"
	BitLockerPIN  = "BitLocker pede PIN na partida (a máquina para na tela do PIN)"

	NoteLaptop      = "notebook: só acorda na tomada e no cabo (ou na dock)"
	NoteFastStartup = "Fast Startup ligado (o desligar vira hibernação; conferir no laboratório)"
	NotePXE         = "a BIOS liga pela rede para o boot PXE: a máquina pode não subir o Windows"
	NoteBIOSUnknown = "a BIOS não informou o Wake on LAN (modelo sem a interface do fabricante?)"
)

// Verdict é a decisão para uma máquina.
type Verdict struct {
	State    string
	Blockers []string
	Notes    []string
}

// Decide: bloqueada com qualquer motivo; pronta com a BIOS e uma placa com cabo confirmadas;
// o resto, sem informação. laptop: o chassi é de notebook.
func Decide(w *protocol.Wake, laptop bool) Verdict {
	var v Verdict
	if w == nil {
		v.State = Unknown
		return v
	}
	block := func(s string) { v.Blockers = append(v.Blockers, s) }
	note := func(s string) { v.Notes = append(v.Notes, s) }
	if w.Adapters != nil {
		wired, magicOK, magicKnown, devOK, devKnown := 0, false, false, false, false
		for _, a := range w.Adapters {
			if !a.Wired {
				continue
			}
			wired++
			if a.MagicPacket != "" {
				magicKnown = true
				magicOK = magicOK || a.MagicPacket == protocol.WakeOn
			}
			if a.DeviceWake != nil {
				devKnown = true
				devOK = devOK || *a.DeviceWake
			}
		}
		switch {
		case wired == 0:
			block(NoWired)
		default:
			if magicKnown && !magicOK {
				block(MagicOff)
			}
			if devKnown && !devOK {
				block(DeviceWakeOff)
			}
		}
	}
	if w.BIOS != nil && !*w.BIOS {
		block(BIOSOff)
	}
	if w.DeepSleep != nil && *w.DeepSleep {
		block(DeepSleepOn)
	}
	if w.BitLockerPIN != nil && *w.BitLockerPIN {
		block(BitLockerPIN)
	}
	if laptop {
		note(NoteLaptop)
	}
	if w.FastStartup != nil && *w.FastStartup {
		note(NoteFastStartup)
	}
	for _, s := range w.Settings {
		if strings.EqualFold(strings.Join(strings.Fields(s.Value), ""), "boottonetwork") {
			note(NotePXE)
			break
		}
	}
	if w.BIOS == nil {
		note(NoteBIOSUnknown)
	}
	switch {
	case len(v.Blockers) > 0:
		v.State = Blocked
	case w.BIOS != nil && *w.BIOS && wiredReady(w.Adapters):
		v.State = Ready
	default:
		v.State = Unknown
	}
	return v
}

// wiredReady: uma placa com cabo com o magic packet ligado e a permissão de ligar (as duas
// confirmadas).
func wiredReady(list []protocol.WakeAdapter) bool {
	for _, a := range list {
		if a.Wired && a.MagicPacket == protocol.WakeOn && a.DeviceWake != nil && *a.DeviceWake {
			return true
		}
	}
	return false
}

// LaptopChassis: os códigos SMBIOS de notebook (portátil, laptop, notebook, subnotebook,
// conversível, destacável).
func LaptopChassis(code string) bool {
	switch strings.TrimSpace(code) {
	case "8", "9", "10", "14", "31", "32":
		return true
	}
	return false
}
