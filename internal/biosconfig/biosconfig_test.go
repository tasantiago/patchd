package biosconfig

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		name     string
		category string // "" = não editável
		risk     bool
	}{
		// Energia e boot.
		{"Wake On LAN", CatPower, false},
		{"Wake on WLAN", CatPower, false},
		{"S5 Maximum Power Savings", CatPower, false},
		{"After Power Loss", CatPower, false},
		{"Fast Boot", CatBoot, false},
		{"USB Storage Boot", CatBoot, false},
		{"Network (PXE) Boot", CatBoot, false},
		{"NumLock on at boot", CatBoot, false},
		// Virtualização e segurança: risco.
		{"Virtualization Technology (VTx)", CatVirtSec, true},
		{"Virtualization Technology for Directed I/O (VTd)", CatVirtSec, true},
		{"Secure Boot", CatVirtSec, true},
		{"Configure Legacy Support and Secure Boot", CatVirtSec, true},
		// Dispositivos.
		{"Integrated Camera", CatDevices, false},
		{"Microphone", CatDevices, false},
		{"Fingerprint Device", CatDevices, false},
		{"Wireless Network Device (WLAN)", CatDevices, true},
		{"Bluetooth", CatDevices, true},
		{"Front USB Ports", CatDevices, true},
		// Exclusões: nunca editáveis.
		{"Setup Password", "", false},
		{"Power-On Password", "", false},
		{"TPM Device", "", false},
		{"TPM State", "", false},
		{"Clear TPM", "", false},
		{"Restore Security Settings to Factory Defaults", "", false},
		{"Enhanced BIOS Authentication Mode", "", false},
		{"Physical Presence Interface", "", false},
		{"Permanent Disable Absolute Persistence Module Set Once", "", false},
		{"BIOS Rollback Policy", "", false},
		{"Lock BIOS Version", "", false},
		{"Sure Start BIOS Settings Protection", "", false},
		{"Clear Secure Boot Keys", "", false},
		{"Import Custom Secure Boot keys", "", false},
		{"Boot Mode", "", false},
		{"Legacy Boot Order", "", false},
		{"UEFI Boot Order", "", false},
		{"Network Interface Controller (LAN)", "", false},
		{"Integrated NIC", "", false},
		// Fora da lista: não editável.
		{"Asset Tracking Number", "", false},
		{"Language", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		r, ok := Lookup(c.name)
		if got := map[bool]string{true: r.Category, false: ""}[ok]; got != c.category || (ok && r.Risk != c.risk) {
			t.Errorf("Lookup(%q) = %q risco=%v, quer %q risco=%v", c.name, got, r.Risk, c.category, c.risk)
		}
	}
}

func TestSecureBootRefusesLegacy(t *testing.T) {
	s := protocol.BIOSSetting{Name: "Configure Legacy Support and Secure Boot", Kind: protocol.BIOSKindEnum,
		Value: "Legacy Support Disable and Secure Boot Enable",
		Possible: []string{"Legacy Support Enable and Secure Boot Disable", "Legacy Support Disable and Secure Boot Enable",
			"Legacy Support Disable and Secure Boot Disable"}}
	r, ok := Editable(s)
	if !ok {
		t.Fatal("deveria ser editável")
	}
	want := []string{"Legacy Support Disable and Secure Boot Enable", "Legacy Support Disable and Secure Boot Disable"}
	if got := r.Choices(s); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Choices = %v", got)
	}
	if err := CheckChange(s, Change{Name: s.Name, Value: "Legacy Support Enable and Secure Boot Disable"}); err == nil {
		t.Fatal("ligar o suporte legado deveria ser recusado")
	}
	if err := CheckChange(s, Change{Name: s.Name, Value: "Legacy Support Disable and Secure Boot Disable"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckChange(t *testing.T) {
	wol := protocol.BIOSSetting{Name: "Wake On LAN", Kind: protocol.BIOSKindEnum, Value: "Disable",
		Possible: []string{"Disable", "Boot to Network", "Boot to Hard Drive"}}
	cases := []struct {
		desc string
		s    protocol.BIOSSetting
		v    string
		ok   bool
	}{
		{"valor possível", wol, "Boot to Hard Drive", true},
		{"valor fora dos possíveis", wol, "Enable", false},
		{"diferença de maiúsculas não passa", wol, "boot to hard drive", false},
		{"vazio", wol, "", false},
		{"só leitura", func() protocol.BIOSSetting { s := wol; s.ReadOnly = true; return s }(), "Disable", false},
		{"texto não é editável", protocol.BIOSSetting{Name: "Wake On LAN", Kind: protocol.BIOSKindString, Value: "x"}, "y", false},
		{"fora da lista", protocol.BIOSSetting{Name: "TPM State", Kind: protocol.BIOSKindEnum, Possible: []string{"Enable", "Disable"}}, "Disable", false},
	}
	for _, c := range cases {
		if err := CheckChange(c.s, Change{Name: c.s.Name, Value: c.v}); (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.desc, err)
		}
	}
}
