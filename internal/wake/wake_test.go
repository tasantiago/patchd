package wake

import (
	"slices"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestDecide(t *testing.T) {
	sim, nao := true, false
	cabo := protocol.WakeAdapter{Name: "Ethernet", Wired: true, MagicPacket: protocol.WakeOn, DeviceWake: &sim}
	wifi := protocol.WakeAdapter{Name: "Wi-Fi", MagicPacket: protocol.WakeOn, DeviceWake: &sim}
	pronta := protocol.Wake{BIOS: &sim, DeepSleep: &nao, Adapters: []protocol.WakeAdapter{cabo, wifi}, BitLockerPIN: &nao}
	if v := Decide(&pronta, false); v.State != Ready || len(v.Blockers) != 0 || len(v.Notes) != 0 {
		t.Errorf("pronta: %+v", v)
	}
	casos := map[string]struct {
		muda   func(*protocol.Wake)
		estado string
		motivo string
	}{
		"BIOS desligada": {func(w *protocol.Wake) { w.BIOS = &nao }, Blocked, BIOSOff},
		"deep sleep":     {func(w *protocol.Wake) { w.DeepSleep = &sim }, Blocked, DeepSleepOn},
		"só Wi-Fi":       {func(w *protocol.Wake) { w.Adapters = []protocol.WakeAdapter{wifi} }, Blocked, NoWired},
		"nenhuma placa":  {func(w *protocol.Wake) { w.Adapters = []protocol.WakeAdapter{} }, Blocked, NoWired},
		"magic desligado": {func(w *protocol.Wake) {
			c := cabo
			c.MagicPacket = protocol.WakeOff
			w.Adapters = []protocol.WakeAdapter{c}
		}, Blocked, MagicOff},
		"sem permissão": {func(w *protocol.Wake) {
			c := cabo
			c.DeviceWake = &nao
			w.Adapters = []protocol.WakeAdapter{c}
		}, Blocked, DeviceWakeOff},
		"PIN":               {func(w *protocol.Wake) { w.BitLockerPIN = &sim }, Blocked, BitLockerPIN},
		"BIOS sem resposta": {func(w *protocol.Wake) { w.BIOS = nil }, Unknown, ""},
		"placas não lidas":  {func(w *protocol.Wake) { w.Adapters = nil }, Unknown, ""},
		"magic sem leitura": {func(w *protocol.Wake) {
			c := cabo
			c.MagicPacket = ""
			w.Adapters = []protocol.WakeAdapter{c}
		}, Unknown, ""},
	}
	for nome, c := range casos {
		w := pronta
		c.muda(&w)
		v := Decide(&w, false)
		if v.State != c.estado || (c.motivo != "" && !slices.Contains(v.Blockers, c.motivo)) {
			t.Errorf("%s: %+v", nome, v)
		}
	}
	if v := Decide(nil, false); v.State != Unknown {
		t.Error("sem coleta")
	}
	w := pronta
	w.FastStartup = &sim
	w.Settings = []protocol.WakeSetting{{Name: "Wake On LAN", Value: "Boot to Network"}}
	v := Decide(&w, true)
	if v.State != Ready || !slices.Contains(v.Notes, NoteLaptop) || !slices.Contains(v.Notes, NoteFastStartup) || !slices.Contains(v.Notes, NotePXE) {
		t.Errorf("avisos: %+v", v)
	}
	if !LaptopChassis("10") || LaptopChassis("3") {
		t.Error("chassi")
	}
}
