package inventory

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestHPBIOSSettings(t *testing.T) {
	rows := []hpSettingRow{
		{protocol.BIOSKindEnum, map[string]any{"Name": "Wake On LAN", "Path": `\Advanced\Power`, "CurrentValue": "Boot to Hard Drive",
			"PossibleValues": []any{"Disable", "Boot to Network", "Boot to Hard Drive", "Disable"}, "IsReadOnly": uint32(0)}},
		{protocol.BIOSKindEnum, map[string]any{"Name": "Secure Boot", "Path": `\Advanced\Secure Boot Configuration`, "CurrentValue": "Enable",
			"PossibleValues": []any{"Disable", "Enable"}, "IsReadOnly": uint32(1)}},
		{protocol.BIOSKindString, map[string]any{"Name": "Asset Tracking Number", "Path": `\Main`, "Value": "PAT-0001\r\n", "IsReadOnly": uint32(0)}},
		{protocol.BIOSKindInteger, map[string]any{"Name": "Startup Delay (sec.)", "Path": `\Advanced\Boot Options`, "IntegerValue": uint32(5), "IsReadOnly": uint32(0)}},
		{protocol.BIOSKindList, map[string]any{"Name": "UEFI Boot Order", "Path": `\Advanced\Boot Options`, "Value": "HDD:M.2:1,NETWORK IPV4:EMBEDDED:1", "IsReadOnly": uint32(0)}},
		// Descartadas: repetida, sem nome, com cara de senha.
		{protocol.BIOSKindEnum, map[string]any{"Name": "Wake On LAN", "CurrentValue": "Disable", "PossibleValues": []any{"Disable"}}},
		{protocol.BIOSKindEnum, map[string]any{"Name": "  ", "CurrentValue": "x"}},
		{protocol.BIOSKindString, map[string]any{"Name": "Setup Password", "Value": "<utf-16/>segredo"}},
	}
	got := hpBIOSSettings(rows)
	if len(got) != 5 {
		t.Fatalf("%d configurações: %+v", len(got), got)
	}
	byName := map[string]protocol.BIOSSetting{}
	for _, s := range got {
		byName[s.Name] = s
	}
	wol := byName["Wake On LAN"]
	if wol.Value != "Boot to Hard Drive" || len(wol.Possible) != 3 || wol.ReadOnly || wol.Kind != protocol.BIOSKindEnum {
		t.Errorf("Wake On LAN: %+v", wol)
	}
	if !byName["Secure Boot"].ReadOnly {
		t.Error("Secure Boot deveria vir só leitura")
	}
	if v := byName["Asset Tracking Number"].Value; v != "PAT-0001" {
		t.Errorf("texto com controle no fim: %q", v)
	}
	if v := byName["Startup Delay (sec.)"].Value; v != "5" {
		t.Errorf("número: %q", v)
	}
	if _, ok := byName["Setup Password"]; ok {
		t.Error("configuração com cara de senha não pode ir ao inventário")
	}
	// Ordem estável: caminho, depois nome.
	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		if a.Path > b.Path || (a.Path == b.Path && a.Name > b.Name) {
			t.Fatalf("fora de ordem: %q antes de %q", a.Name, b.Name)
		}
	}
	// O resultado passa na conferência do servidor.
	fw := protocol.Firmware{Interface: protocol.FirmwareHP, AdminPassword: new(bool), Settings: got}
	if err := fw.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestHPBIOSSettingsLimits(t *testing.T) {
	var rows []hpSettingRow
	long := strings.Repeat("x", 1000)
	many := make([]any, 200)
	for i := range many {
		many[i] = strings.Repeat("v", i+1)
	}
	for i := 0; i < protocol.MaxBIOSSettings+50; i++ {
		rows = append(rows, hpSettingRow{protocol.BIOSKindEnum, map[string]any{
			"Name":         long[:10] + strings.Repeat("y", i%7) + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676)),
			"CurrentValue": long, "PossibleValues": many}})
	}
	got := hpBIOSSettings(rows)
	if len(got) != protocol.MaxBIOSSettings {
		t.Fatalf("%d configurações", len(got))
	}
	for _, s := range got {
		if len(s.Value) > protocol.MaxBIOSValue || len(s.Possible) > protocol.MaxBIOSPossible {
			t.Fatalf("fora dos limites: valor %d, possíveis %d", len(s.Value), len(s.Possible))
		}
	}
	fw := protocol.Firmware{Interface: protocol.FirmwareHP, AdminPassword: new(bool), Settings: got}
	if err := fw.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsFirmwareInfoSettingsOnlyHP(t *testing.T) {
	set := []hpSettingRow{{protocol.BIOSKindEnum, map[string]any{"Name": "Fast Boot", "CurrentValue": "Enable", "PossibleValues": []any{"Disable", "Enable"}}}}
	hp := windowsFirmwareInfo(wmiFirmware{Interface: "hp", Passwords: []map[string]any{{"Name": "Setup Password", "IsSet": int32(1)}},
		SettingsQueried: true, Settings: set})
	if hp == nil || len(hp.Settings) != 1 {
		t.Fatalf("HP: %+v", hp)
	}
	// Sem a interface confirmada (as senhas não vieram), as configurações não vão.
	none := windowsFirmwareInfo(wmiFirmware{Interface: "hp", SettingsQueried: true, Settings: set})
	if none != nil && none.Settings != nil {
		t.Fatalf("sem interface: %+v", none)
	}
}
