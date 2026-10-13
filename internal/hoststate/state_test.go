package hoststate

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

var testTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func mac(s string) net.HardwareAddr {
	m, _ := net.ParseMAC(s)
	return m
}

func pfx(l ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range l {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestSelectInterfaces(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	raw := []netIf{
		{name: "lo", flags: net.FlagUp | net.FlagLoopback, addrs: pfx("127.0.0.1/8", "::1/128")},
		{name: "wlan0", mac: mac("00:11:22:33:44:66"), addrs: pfx("169.254.7.7/16")}, // parada: fica (MAC de fabricante), sem o 169.254
		{name: "veth1a2b", mac: mac("6a:1f:00:aa:bb:cc"), flags: up},                 // veth de container: sai
		{name: "tun0", flags: up}, // sem MAC e sem endereço: sai
		{name: "eth0", mac: mac("00:15:5d:01:02:03"), flags: up, addrs: pfx("192.0.2.10/24", "fe80::215:5dff:fe01:203/64", "2001:db8::10/64")},
		{name: "docker0", mac: mac("02:42:ac:11:00:01"), flags: up, addrs: pfx("172.17.0.1/16")},
	}
	got := selectInterfaces(raw)
	nomes := []string{}
	for _, n := range got {
		nomes = append(nomes, n.Name)
	}
	if len(got) != 3 || nomes[0] != "docker0" || nomes[1] != "eth0" || nomes[2] != "wlan0" {
		t.Fatalf("interfaces: %v (quer as ativas em ordem de nome, depois as paradas)", nomes)
	}
	eth := got[1]
	if eth.MAC != "00:15:5d:01:02:03" || eth.LocalMAC || !eth.Up ||
		len(eth.IPv4) != 1 || eth.IPv4[0] != "192.0.2.10/24" || len(eth.IPv6) != 1 || eth.IPv6[0] != "2001:db8::10/64" {
		t.Errorf("eth0 (o fe80:: fica de fora): %+v", eth)
	}
	if len(got[2].IPv4) != 0 {
		t.Errorf("o 169.254 de uma placa parada sai: %v", got[2].IPv4)
	}
	// Placa ativa sem DHCP: o 169.254 fica (mostra o problema).
	if l := selectInterfaces([]netIf{{name: "eth9", mac: mac("00:11:22:33:44:77"), flags: up, addrs: pfx("169.254.1.2/16")}}); len(l) != 1 || len(l[0].IPv4) != 1 {
		t.Errorf("169.254 numa placa ativa: %+v", l)
	}
	if !got[0].LocalMAC || got[2].Up {
		t.Errorf("docker0 com MAC local, wlan0 parada: %+v %+v", got[0], got[2])
	}
	st := protocol.MachineState{CollectedAt: testTime, Interfaces: got}
	if err := st.Check(); err != nil {
		t.Errorf("o que o agente monta passa na conferência do servidor: %v", err)
	}
}

func TestDedupe(t *testing.T) {
	in := []protocol.Disk{
		{Mount: "/home", Device: "/dev/sda2", TotalBytes: 10},
		{Mount: "/", Device: "/dev/sda2", TotalBytes: 10}, // subvolume/bind do mesmo dispositivo
		{Mount: "/boot/efi", Device: "/dev/sda1", TotalBytes: 1},
		{Mount: "C:", TotalBytes: 5}, // Windows: sem dispositivo, não junta
	}
	got := dedupe(in)
	if len(got) != 3 || got[0].Mount != "/" || got[1].Mount != "/boot/efi" || got[2].Mount != "C:" {
		t.Errorf("dedupe: %+v", got)
	}
}

func TestTrim(t *testing.T) {
	s := protocol.MachineState{CollectedAt: testTime}
	for range protocol.MaxStateInterfaces + 10 {
		n := protocol.NetInterface{Name: "veth", Up: true}
		for range protocol.MaxStateAddrs + 3 {
			n.IPv4 = append(n.IPv4, "192.0.2.1/32")
		}
		s.Interfaces = append(s.Interfaces, n)
	}
	trim(&s)
	if err := s.Check(); err != nil {
		t.Errorf("depois do corte, o servidor aceita: %v", err)
	}
}

// A coleta de verdade, nesta máquina: não falha e passa na conferência do servidor.
func TestCollectNestaMaquina(t *testing.T) {
	s := Collect(context.Background())
	if err := s.Check(); err != nil {
		t.Fatalf("estado coletado aqui não passa na conferência: %v", err)
	}
	t.Logf("%d interfaces, %d discos, erros: %v", len(s.Interfaces), len(s.Disks), s.Errors)
}
