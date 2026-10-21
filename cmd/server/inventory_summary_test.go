package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestResumoDaFrota(t *testing.T) {
	dir := t.TempDir()
	sim, nao := true, false
	cabo := protocol.WakeAdapter{Name: "Ethernet", Wired: true, MagicPacket: protocol.WakeOn, DeviceWake: &sim}
	rep := func(host string, fw *protocol.Firmware, w *protocol.Wake) []byte {
		b, _ := json.MarshalIndent(protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:x", OS: protocol.OSInfo{Family: "windows", Hostname: host},
			Hardware: &protocol.Hardware{Manufacturer: "HP", ChassisType: "3", Sources: []string{}, Firmware: fw, Wake: w}}, "", "  ")
		return b
	}
	utf16le := func(b []byte) []byte {
		out := []byte{0xFF, 0xFE}
		for _, u := range utf16.Encode([]rune(string(b))) {
			out = binary.LittleEndian.AppendUint16(out, u)
		}
		return out
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// PowerShell 5 (UTF-16), PowerShell 7 com BOM, UTF-8 puro, e um arquivo estragado.
	write("a.json", utf16le(rep("pc-exemplo-01", &protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &nao, SecureBoot: &nao, TPM: "2.0"},
		&protocol.Wake{BIOS: &sim, Adapters: []protocol.WakeAdapter{cabo}})))
	write("b.json", append([]byte{0xEF, 0xBB, 0xBF}, rep("pc-exemplo-02", &protocol.Firmware{Interface: "hp", AdminPassword: &nao, SureAdmin: &nao, TPM: "2.0"},
		&protocol.Wake{BIOS: &nao, Adapters: []protocol.WakeAdapter{cabo}})...))
	write("c.json", rep("pc-exemplo-03", nil, nil))
	write("d.json", []byte("Acesso negado"))
	var o, e bytes.Buffer
	if code := run([]string{"inventory", "summary", "-dir", dir}, mapa(nil), &o, &e); code != exitOK {
		t.Fatalf("%d %s", code, e.String())
	}
	out := o.String()
	for _, quer := range []string{"inventários lidos: 3 de 4 arquivos (1 ilegíveis", "Senha de administrador da BIOS\n",
		"     1  definida", "     1  NÃO definida", "     1  sem informação", "HP Sure Admin (só HP)\n     2  desligado",
		"Ligar pela rede\n", "     1  pronta", "     1  bloqueada", "     1  Wake on LAN desligado na BIOS",
		"Interface da BIOS (fabricante)\n     2  hp\n     1  nenhuma"} {
		if !strings.Contains(out, quer) {
			t.Errorf("faltou %q em:\n%s", quer, out)
		}
	}
	if strings.Contains(out, "pc-exemplo") {
		t.Error("nome de máquina sem -maquinas")
	}
	o.Reset()
	run([]string{"inventory", "summary", "-dir", dir, "-maquinas"}, mapa(nil), &o, &e)
	if !strings.Contains(o.String(), "pc-exemplo-02") || !strings.Contains(o.String(), "fica com você") {
		t.Errorf("-maquinas:\n%s", o.String())
	}
	if code := run([]string{"inventory", "summary", "-dir", t.TempDir()}, mapa(nil), &o, &e); code != exitConfig {
		t.Errorf("pasta vazia: %d", code)
	}
	if code := run([]string{"inventory"}, mapa(nil), &o, &e); code != exitConfig {
		t.Errorf("sem subcomando: %d", code)
	}
}
