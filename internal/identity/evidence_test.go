package identity

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestNormalizeInstallID(t *testing.T) {
	casos := map[string]string{
		"{3F2504E0-4F89-11D3-9A0C-0305E82C3301}": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		" 4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b\n":    "4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b",
	}
	for in, quer := range casos {
		if got, ok := normalizeInstallID(in); !ok || got != quer {
			t.Errorf("normalizeInstallID(%q) = %q, %t", in, got, ok)
		}
	}
	for _, ruim := range []string{"", "uninitialized", "00000000000000000000000000000000", "{00000000-0000-0000-0000-000000000000}"} {
		if _, ok := normalizeInstallID(ruim); ok {
			t.Errorf("deveria descartar %q", ruim)
		}
	}
}

func TestNormalizeUUIDEUUIDKey(t *testing.T) {
	u, ok := NormalizeUUID("{4C4C4544-0042-3510-8051-B7C04F4B4E32}")
	if !ok || u != "4c4c4544-0042-3510-8051-b7c04f4b4e32" {
		t.Fatalf("NormalizeUUID = %q, %t", u, ok)
	}
	if swapUUID(u) != "44454c4c-4200-1035-8051-b7c04f4b4e32" {
		t.Errorf("swap errado: %s", swapUUID(u))
	}
	if UUIDKey(u) != UUIDKey(swapUUID(u)) {
		t.Error("o mesmo UUID nas duas ordens de bytes precisa dar a mesma chave")
	}
	for _, ruim := range []string{"", "nao-e-uuid", "00000000-0000-0000-0000-000000000000",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF", "03000200-0400-0500-0006-000700080009"} {
		if _, ok := NormalizeUUID(ruim); ok {
			t.Errorf("deveria descartar %q", ruim)
		}
	}
}

func TestNormalizeSerial(t *testing.T) {
	if got, ok := normalizeSerial("  pf3abc12 "); !ok || got != "PF3ABC12" {
		t.Errorf("serial válido: %q %t", got, ok)
	}
	if got, ok := normalizeSerial("VMware-56 4d 1a 2b"); !ok || got != "VMWARE-56 4D 1A 2B" {
		t.Errorf("serial da VMware é único e válido: %q %t", got, ok)
	}
	for _, ruim := range []string{"To be filled by O.E.M.", "Default string", "System Serial Number",
		"0", "abc", "00000000", "XXXXXXXX", "123456789", "  n/a  "} {
		if _, ok := normalizeSerial(ruim); ok {
			t.Errorf("deveria descartar %q", ruim)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	if got, ok := NormalizeMAC("00-1A-2B-3C-4D-5E"); !ok || got != "00:1a:2b:3c:4d:5e" {
		t.Errorf("MAC de fábrica: %q %t", got, ok)
	}
	for _, ruim := range []string{
		"02:42:ac:11:00:02", // Docker (administrado localmente)
		"da:a1:19:00:00:01", // aleatório do Wi-Fi (bit 0x02)
		"01:00:5e:00:00:fb", // multicast
		"00:00:00:00:00:00", "00:1a:2b", "zz:1a:2b:3c:4d:5e",
	} {
		if _, ok := NormalizeMAC(ruim); ok {
			t.Errorf("deveria descartar %q", ruim)
		}
	}
}

func TestNormalizeRegistraDescartes(t *testing.T) {
	k, desc := Normalize(protocol.IdentityEvidence{
		InstallID:    "uninitialized",
		HardwareUUID: "03000200-0400-0500-0006-000700080009",
		Serial:       "To be filled by O.E.M.",
	})
	if k != (Keys{}) || k.Strong() || len(desc) != 3 {
		t.Errorf("tudo genérico: chaves vazias e três descartes: %+v %v", k, desc)
	}
	if !strings.Contains(strings.Join(desc, "|"), "serial genérico") {
		t.Errorf("o motivo do descarte deve aparecer: %v", desc)
	}
}

func TestRelation(t *testing.T) {
	a := Keys{InstallID: "i1", HardwareKey: "h1", Serial: "S1"}
	casos := []struct {
		nome string
		nova Keys
		quer string
	}{
		{"agente reinstalado", Keys{InstallID: "i1", HardwareKey: "h1", Serial: "S1"}, RelationReenrollment},
		{"mesma instalação sem UUID legível", Keys{InstallID: "i1"}, RelationReenrollment},
		{"imagem clonada", Keys{InstallID: "i1", HardwareKey: "h2", Serial: "S2"}, RelationClone},
		{"SO reinstalado", Keys{InstallID: "i9", HardwareKey: "h1"}, RelationSameHardware},
		{"só o serial casa", Keys{InstallID: "i9", Serial: "S1"}, RelationSameHardware},
		{"serial igual mas UUID diferente", Keys{InstallID: "i9", HardwareKey: "h2", Serial: "S1"}, ""},
		{"nada em comum", Keys{InstallID: "i9", HardwareKey: "h9"}, ""},
		{"tudo vazio", Keys{}, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, ok := Relation(c.nova, a)
			if got != c.quer || ok != (c.quer != "") {
				t.Errorf("Relation = %q %t, esperado %q", got, ok, c.quer)
			}
		})
	}
}
