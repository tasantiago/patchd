package inventory

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestConfiguracoesDoFirmware(t *testing.T) {
	hp := []map[string]any{
		{"Name": "S5 Wake On LAN", "CurrentValue": "Enable"},
		{"Name": "S5 Maximum Power Savings", "CurrentValue": "Disable"},
		{"Name": "Wake On LAN", "CurrentValue": "Boot to Hard Drive"},
		{"Name": "Num Lock State at Power-On", "CurrentValue": "On"}, // fora
	}
	s := biosSettings(protocol.FirmwareHP, hp)
	if len(s) != 3 || s[0].Name != "S5 Maximum Power Savings" {
		t.Fatalf("HP: %+v", s)
	}
	bios, deep := wakeFirmware(s)
	if bios == nil || !*bios || deep == nil || *deep {
		t.Errorf("HP: bios=%v deep=%v", bios, deep)
	}
	dell := []map[string]any{
		{"AttributeName": "WakeOnLan", "CurrentValue": []any{"LanOnly"}},
		{"AttributeName": "DeepSleepCtrl", "CurrentValue": []any{"S4AndS5"}},
		{"AttributeName": "WakeOnWlan", "CurrentValue": []any{"Enabled"}},
	}
	bios, deep = wakeFirmware(biosSettings(protocol.FirmwareDell, dell))
	if bios == nil || !*bios || deep == nil || !*deep {
		t.Errorf("Dell: bios=%v deep=%v", bios, deep)
	}
	lenovo := []map[string]any{
		{"CurrentSetting": "WakeOnLAN,Disable;[Optional:Disable,ACOnly,ACandBattery,Enable]"},
		{"CurrentSetting": "WakeOnLANDock,Enable"},
		{"CurrentSetting": "USBPortAccess,Enable"},
	}
	ls := biosSettings(protocol.FirmwareLenovo, lenovo)
	bios, deep = wakeFirmware(ls)
	if len(ls) != 2 || bios == nil || *bios || deep != nil {
		t.Errorf("Lenovo (só o da dock ligado): %+v bios=%v", ls, bios)
	}
	// Valor que não se conhece: não se sabe, em vez de um palpite.
	if bios, _ := wakeFirmware([]protocol.WakeSetting{{Name: "Wake on LAN", Value: "Talvez"}}); bios != nil {
		t.Error("valor desconhecido virou decisão")
	}
}

func TestPlacasDeRede(t *testing.T) {
	nets := []map[string]any{
		{"Name": "Ethernet", "MacAddress": "00-11-22-33-44-55", "NdisPhysicalMedium": uint32(14), "HardwareInterface": true, "Virtual": false,
			"PnPDeviceID": `PCI\VEN_8086&DEV_15BB\3&11583659&0&FE`},
		{"Name": "Wi-Fi", "MacAddress": "00-11-22-33-44-66", "NdisPhysicalMedium": uint32(9), "HardwareInterface": true, "Virtual": false,
			"PnPDeviceID": `PCI\VEN_8086&DEV_2723\4&1&0&00E0`},
		{"Name": "vEthernet (WSL)", "MacAddress": "00-15-5D-00-00-01", "NdisPhysicalMedium": uint32(14), "HardwareInterface": false, "Virtual": true},
	}
	power := []map[string]any{{"Name": "Ethernet", "WakeOnMagicPacket": uint8(2)}, {"Name": "Wi-Fi", "WakeOnMagicPacket": uint8(0)}}
	dw := []map[string]any{{"InstanceName": `PCI\VEN_8086&DEV_15BB\3&11583659&0&FE_0`, "Enable": true}}
	got := wakeAdapters(nets, power, dw)
	if len(got) != 2 {
		t.Fatalf("placas físicas: %+v", got)
	}
	e, w := got[0], got[1]
	if e.Name != "Ethernet" || !e.Wired || e.MAC != "00:11:22:33:44:55" || e.MagicPacket != protocol.WakeOn || e.DeviceWake == nil || !*e.DeviceWake {
		t.Errorf("cabo: %+v", e)
	}
	if w.Wired || w.MagicPacket != protocol.WakeUnsupported || w.DeviceWake != nil {
		t.Errorf("Wi-Fi: %+v", w)
	}
	sim := true
	info := windowsWakeInfo(wmiWake{Vendor: protocol.FirmwareHP, BIOSRows: []map[string]any{{"Name": "S5 Wake On LAN", "CurrentValue": "Enable"}},
		Adapters: nets, Power: power, DeviceWake: dw, FastStartup: &sim})
	if info == nil || info.BIOS == nil || len(info.Adapters) != 2 || info.FastStartup == nil || info.Check() != nil {
		t.Errorf("tudo junto: %+v", info)
	}
	if windowsWakeInfo(wmiWake{}) != nil {
		t.Error("nada lido deveria ser nil")
	}
	if got := windowsWakeInfo(wmiWake{Adapters: []map[string]any{}}); got == nil || got.Adapters == nil || len(got.Adapters) != 0 {
		t.Errorf("lido e sem placa: %+v", got)
	}
}
