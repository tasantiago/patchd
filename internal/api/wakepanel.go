package api

import (
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wake"
)

// O cartão "Ligar pela rede" na aba Hardware (Aula 22.1): a decisão do internal/wake e o
// que pesou nela, para conferir no laboratório.

type wakeCard struct {
	State, Level string
	Blockers     []string
	Notes        []string
	Adapters     []wakeAdapterRow
	Settings     []protocol.WakeSetting
	FastStartup  string
	BitLocker    string
}

type wakeAdapterRow struct {
	Name, MAC, Wired, Magic, DeviceWake string
}

func wakeView(hw protocol.Hardware) *wakeCard {
	if hw.Wake == nil {
		return nil
	}
	w := hw.Wake
	v := wake.Decide(w, wake.LaptopChassis(hw.ChassisType))
	c := &wakeCard{State: v.State, Blockers: v.Blockers, Notes: v.Notes, Settings: w.Settings}
	switch v.State {
	case wake.Ready:
		c.Level = "ok"
	case wake.Blocked:
		c.Level = "erro"
	default:
		c.Level = "atencao"
	}
	yesNo := func(p *bool, yes, no string) string {
		switch {
		case p == nil:
			return "—"
		case *p:
			return yes
		}
		return no
	}
	magic := map[string]string{protocol.WakeOn: "ligado", protocol.WakeOff: "desligado", protocol.WakeUnsupported: "sem suporte", "": "—"}
	for _, a := range w.Adapters {
		wired := "não (Wi-Fi ou outra)"
		if a.Wired {
			wired = "sim"
		}
		c.Adapters = append(c.Adapters, wakeAdapterRow{Name: a.Name, MAC: dash(a.MAC), Wired: wired, Magic: magic[a.MagicPacket],
			DeviceWake: yesNo(a.DeviceWake, "sim", "não")})
	}
	c.FastStartup = yesNo(w.FastStartup, "ligado", "desligado")
	c.BitLocker = yesNo(w.BitLockerPIN, "pede PIN na partida", "sem PIN na partida")
	return c
}
