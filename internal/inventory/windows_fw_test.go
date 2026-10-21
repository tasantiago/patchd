package inventory

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestFirmwareInterface(t *testing.T) {
	for in, quer := range map[string]string{"HP": "hp", "Hewlett-Packard": "hp", "Dell Inc.": "dell", "LENOVO": "lenovo",
		"Positivo Tecnologia SA": "", "VMware, Inc.": "", "": ""} {
		if got := firmwareInterface(in); got != quer {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestWindowsFirmwareInfo(t *testing.T) {
	sim, nao := true, false
	casos := []struct {
		nome string
		in   wmiFirmware
		quer *protocol.Firmware
	}{
		{"nada lido", wmiFirmware{}, nil},
		{"HP com senha de setup", wmiFirmware{FirmwareType: 2, SecureBoot: &sim, TPMQueried: true,
			TPM: map[string]any{"SpecVersion": "2.0, 0, 1.59"}, Interface: "hp",
			Passwords: []map[string]any{{"Name": "Setup Password", "IsSet": int32(1)}, {"Name": "Power-On Password", "IsSet": int32(0)}}},
			&protocol.Firmware{Mode: "uefi", SecureBoot: &sim, TPM: "2.0", Interface: "hp", AdminPassword: &sim, PowerOnPassword: &nao}},
		{"Dell sem senha", wmiFirmware{FirmwareType: 2, SecureBoot: &nao, Interface: "dell",
			Passwords: []map[string]any{{"NameId": "Admin", "IsPasswordSet": uint32(0)}, {"NameId": "System", "IsPasswordSet": uint32(0)}}},
			&protocol.Firmware{Mode: "uefi", SecureBoot: &nao, Interface: "dell", AdminPassword: &nao, PowerOnPassword: &nao}},
		{"Lenovo supervisor", wmiFirmware{FirmwareType: 2, Interface: "lenovo", LenovoState: map[string]any{"PasswordState": int32(2)}},
			&protocol.Firmware{Mode: "uefi", Interface: "lenovo", AdminPassword: &sim, PowerOnPassword: &nao}},
		{"Lenovo ligar e supervisor e disco", wmiFirmware{Interface: "lenovo", LenovoState: map[string]any{"PasswordState": int32(7)}},
			&protocol.Firmware{Interface: "lenovo", AdminPassword: &sim, PowerOnPassword: &sim}},
		{"legado, sem TPM, sem interface", wmiFirmware{FirmwareType: 1, TPMQueried: true, Interface: "hp"},
			&protocol.Firmware{Mode: "legado", SecureBoot: &nao, TPM: "nenhum"}},
		{"TPM estranho", wmiFirmware{TPMQueried: true, TPM: map[string]any{"SpecVersion": "9.9"}}, nil},
		// Aula 21.5: o HP Sure Admin.
		{"HP com Sure Admin", wmiFirmware{Interface: "hp", Passwords: []map[string]any{{"Name": "Setup Password", "IsSet": int32(1)}},
			SureAdminQueried: true, SureAdmin: map[string]any{"CurrentValue": "Enable"}},
			&protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &sim}},
		{"HP sem a configuração", wmiFirmware{Interface: "hp", Passwords: []map[string]any{{"Name": "Setup Password", "IsSet": int32(1)}},
			SureAdminQueried: true}, &protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &nao}},
		{"HP, consulta não rodou", wmiFirmware{Interface: "hp", Passwords: []map[string]any{{"Name": "Setup Password", "IsSet": int32(0)}}},
			&protocol.Firmware{Interface: "hp", AdminPassword: &nao}},
	}
	for _, c := range casos {
		got := windowsFirmwareInfo(c.in)
		switch {
		case got == nil && c.quer == nil:
		case got == nil || c.quer == nil:
			t.Errorf("%s: %+v, quer %+v", c.nome, got, c.quer)
		default:
			if txt(got) != txt(c.quer) {
				t.Errorf("%s:\n got %s\nquer %s", c.nome, txt(got), txt(c.quer))
			}
			if err := got.Check(); err != nil {
				t.Errorf("%s: Check: %v", c.nome, err)
			}
		}
	}
}

func txt(f *protocol.Firmware) string {
	b := func(p *bool) string {
		if p == nil {
			return "?"
		}
		if *p {
			return "s"
		}
		return "n"
	}
	return f.Mode + "|" + b(f.SecureBoot) + "|" + f.TPM + "|" + f.Interface + "|" + b(f.AdminPassword) + "|" + b(f.PowerOnPassword) +
		"|" + b(f.SureAdmin)
}
