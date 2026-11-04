package wake

import (
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func estado(relay bool, ifaces ...protocol.NetInterface) *protocol.MachineState {
	return &protocol.MachineState{Interfaces: ifaces, WakeRelay: relay}
}

func placa(mac string, ips ...string) protocol.NetInterface {
	return protocol.NetInterface{Name: "eth0", MAC: mac, IPv4: ips, Up: true}
}

func TestBuild(t *testing.T) {
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	alvo := estado(false, placa("00:11:22:33:44:55", "192.0.2.20/24"),
		protocol.NetInterface{Name: "vpn", MAC: "02:00:00:00:00:01", IPv4: []string{"10.8.0.5/24"}, Up: true, LocalMAC: true})
	w := &protocol.Wake{Adapters: []protocol.WakeAdapter{
		{Name: "Ethernet", MAC: "00-11-22-33-44-55", Wired: true, MagicPacket: protocol.WakeOn},
		{Name: "Wi-Fi", MAC: "00:11:22:33:44:66", Wired: false},
		{Name: "Dock", MAC: "00:11:22:33:44:77", Wired: true, MagicPacket: protocol.WakeOff},
	}}
	cand := []Host{
		{ID: "velho", LastSeen: now.Add(-3 * time.Hour), State: estado(true, placa("00:aa:00:00:00:01", "192.0.2.30/24"))},
		{ID: "antigo-sem-suporte", LastSeen: now.Add(-time.Minute), State: estado(false, placa("00:aa:00:00:00:02", "192.0.2.31/24"))},
		{ID: "outra-rede", LastSeen: now.Add(-time.Minute), State: estado(true, placa("00:aa:00:00:00:03", "198.51.100.4/24"))},
		{ID: "b", LastSeen: now.Add(-30 * time.Minute), State: estado(true, placa("00:aa:00:00:00:04", "192.0.2.40/24"))},
		{ID: "a", LastSeen: now.Add(-5 * time.Minute), State: estado(true, placa("00:aa:00:00:00:05", "192.0.2.41/24"))},
		{ID: "c", LastSeen: now.Add(-50 * time.Minute), State: estado(true, placa("00:aa:00:00:00:06", "192.0.2.42/24"))},
		{ID: "d", LastSeen: now.Add(-90 * time.Minute), State: estado(true, placa("00:aa:00:00:00:07", "192.0.2.43/24"))},
		{ID: "prefixo-diferente", LastSeen: now, State: estado(true, placa("00:aa:00:00:00:08", "192.0.2.44/23"))},
	}
	p, err := Build(w, alvo, cand, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MACs) != 1 || p.MACs[0] != "00:11:22:33:44:55" || p.Network.String() != "192.0.2.0/24" {
		t.Fatalf("%+v", p)
	}
	if len(p.Relays) != 3 || p.Relays[0].ID != "a" || p.Relays[1].ID != "b" || p.Relays[2].ID != "c" {
		t.Fatalf("repetidores %+v", p.Relays)
	}

	// Sem o bloco wake: o MAC da placa de fabricante com IPv4 (a da VPN não).
	if m := MACs(nil, alvo); len(m) != 1 || m[0] != "00:11:22:33:44:55" {
		t.Fatalf("MACs sem wake: %v", m)
	}
	if _, err := Build(nil, nil, cand, now); !errors.Is(err, ErrNoMAC) {
		t.Fatalf("sem estado: %v", err)
	}
	var nr NoRelayError
	if _, err := Build(w, alvo, cand[:3], now); !errors.As(err, &nr) || len(nr.Networks) != 1 {
		t.Fatalf("sem repetidor: %v", err)
	}
	if _, err := Build(w, estado(false, protocol.NetInterface{MAC: "00:11:22:33:44:55", IPv4: []string{"169.254.1.2/16"}}), cand, now); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("só link-local: %v", err)
	}
}
