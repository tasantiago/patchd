package biosvendor

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestDell(t *testing.T) {
	got := DellSettings([]DellRow{
		{protocol.BIOSKindEnum, map[string]any{"AttributeName": "WakeOnLan", "CurrentValue": []any{"Disabled"},
			"PossibleValue": []any{"Disabled", "LanOnly", "LanWlan", "Disabled"}, "IsReadonly": uint32(0)}},
		{protocol.BIOSKindEnum, map[string]any{"AttributeName": "SecureBoot", "CurrentValue": []any{"Enabled"},
			"PossibleValue": []any{"Disabled", "Enabled"}, "IsReadonly": uint32(1)}},
		{protocol.BIOSKindInteger, map[string]any{"AttributeName": "AutoOnHr", "CurrentValue": []any{int32(7)}}},
		{protocol.BIOSKindString, map[string]any{"AttributeName": "Asset", "CurrentValue": []any{"PAT-0002\r\n"}}},
		{protocol.BIOSKindString, map[string]any{"AttributeName": "AdminPassword", "CurrentValue": []any{"x"}}},
		{protocol.BIOSKindEnum, map[string]any{"AttributeName": "WakeOnLan", "CurrentValue": []any{"LanOnly"}}},
		{protocol.BIOSKindEnum, map[string]any{"CurrentValue": []any{"x"}}},
	})
	by := map[string]protocol.BIOSSetting{}
	for _, s := range got {
		by[s.Name] = s
	}
	if len(got) != 4 {
		t.Fatalf("%d: %+v", len(got), got)
	}
	if w := by["WakeOnLan"]; w.Value != "Disabled" || len(w.Possible) != 3 || w.ReadOnly || w.Kind != protocol.BIOSKindEnum {
		t.Errorf("WakeOnLan: %+v", w)
	}
	if !by["SecureBoot"].ReadOnly || by["AutoOnHr"].Value != "7" || by["Asset"].Value != "PAT-0002" || by["Asset"].Possible != nil {
		t.Errorf("%+v", by)
	}
	fw := protocol.Firmware{Interface: protocol.FirmwareDell, AdminPassword: new(bool), Settings: got}
	if err := fw.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestLenovo(t *testing.T) {
	currents := []string{
		"WakeOnLAN,ACOnly;[Optional:Disable,ACOnly,ACandBattery,Enable]",
		"SecureBoot,Enable;[Optional:Disable,Enable][Status:ShowOnly]",
		"VirtualizationTechnology,Disable",
		"BootOrder,USBHDD:NVMe0:PXEBOOT",
		"",
		",semnome",
		"SupervisorPassword,Enable",
	}
	s, ok := LenovoSetting(currents[0])
	if !ok || s.Name != "WakeOnLAN" || s.Value != "ACOnly" || len(s.Possible) != 4 || s.Kind != protocol.BIOSKindEnum || s.ReadOnly {
		t.Fatalf("%+v", s)
	}
	if s, _ := LenovoSetting(currents[1]); !s.ReadOnly || len(s.Possible) != 2 {
		t.Errorf("ShowOnly: %+v", s)
	}
	need := LenovoNeedSelections(currents, func(n string) bool { return strings.Contains(n, "Virtualization") || n == "BootOrder" })
	if strings.Join(need, ",") != "VirtualizationTechnology,BootOrder" {
		t.Errorf("seleções: %v", need)
	}
	got := LenovoSettings(currents, map[string]string{"VirtualizationTechnology": "Disable,Enable"})
	by := map[string]protocol.BIOSSetting{}
	for _, s := range got {
		by[s.Name] = s
	}
	if len(got) != 4 {
		t.Fatalf("%d: %+v", len(got), got)
	}
	if v := by["VirtualizationTechnology"]; v.Kind != protocol.BIOSKindEnum || len(v.Possible) != 2 {
		t.Errorf("seleção: %+v", v)
	}
	if b := by["BootOrder"]; b.Kind != protocol.BIOSKindString || b.Value != "USBHDD:NVMe0:PXEBOOT" {
		t.Errorf("texto: %+v", b)
	}
	fw := protocol.Firmware{Interface: protocol.FirmwareLenovo, AdminPassword: new(bool), Settings: got}
	if err := fw.Check(); err != nil {
		t.Fatal(err)
	}
}
