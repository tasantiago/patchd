package wol

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseMAC(t *testing.T) {
	for _, ok := range []string{"00:11:22:33:44:55", "00-11-22-33-44-55", " a4:bb:6d:01:02:03 "} {
		if _, err := ParseMAC(ok); err != nil {
			t.Errorf("%q recusado: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:01", "00:11:22:33:44", "00:11:22:33:44:55:66:77", "zz:11:22:33:44:55"} {
		if _, err := ParseMAC(bad); err == nil {
			t.Errorf("%q aceito", bad)
		}
	}
}

func TestPacket(t *testing.T) {
	mac, _ := ParseMAC("00:11:22:33:44:55")
	p := Packet(mac)
	if len(p) != 102 || !bytes.Equal(p[:6], []byte{255, 255, 255, 255, 255, 255}) {
		t.Fatalf("cabeça %x (%d bytes)", p[:6], len(p))
	}
	for i := range 16 {
		if !bytes.Equal(p[6+6*i:12+6*i], mac) {
			t.Fatalf("cópia %d do MAC errada", i)
		}
	}
}

func TestBroadcast(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.10/24": "192.0.2.255", "10.1.2.3/16": "10.1.255.255", "172.16.5.1/22": "172.16.7.255", "198.51.100.9/30": "198.51.100.11"} {
		bc, ok := Iface{Prefix: netip.MustParsePrefix(in)}.Broadcast()
		if !ok || bc.String() != want {
			t.Errorf("%s → %v %v, quero %s", in, bc, ok, want)
		}
	}
	for _, in := range []string{"192.0.2.1/31", "192.0.2.1/32", "10.0.0.1/7", "2001:db8::1/64"} {
		if _, ok := (Iface{Prefix: netip.MustParsePrefix(in)}).Broadcast(); ok {
			t.Errorf("%s com broadcast", in)
		}
	}
}

type envio struct{ from, to string }

func fake(ifaces []Iface, fail map[string]bool) (*Sender, *[]envio) {
	var sent []envio
	return &Sender{
		Ifaces: func() ([]Iface, error) { return ifaces, nil },
		Send: func(_ context.Context, from, to netip.Addr, p []byte) error {
			if fail[to.String()] {
				return errors.New("rede inacessível")
			}
			if len(p) != 102 {
				return errors.New("pacote errado")
			}
			sent = append(sent, envio{from.String(), to.String()})
			return nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}, &sent
}

func TestWake(t *testing.T) {
	ifaces := []Iface{
		{Name: "eth0", Prefix: netip.MustParsePrefix("192.0.2.10/24")},
		{Name: "eth1", Prefix: netip.MustParsePrefix("198.51.100.7/24")},
		{Name: "vpn", Prefix: netip.MustParsePrefix("10.9.0.2/32")},
	}
	macs := []net.HardwareAddr{{0, 1, 2, 3, 4, 5}, {0, 1, 2, 3, 4, 6}}
	s, sent := fake(ifaces, nil)
	res, err := s.Wake(context.Background(), macs, netip.MustParsePrefix("192.0.2.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2*Repeats || len(*sent) != 2*Repeats || len(res.Networks) != 1 || !strings.Contains(res.Networks[0], "192.0.2.255") {
		t.Fatalf("%+v %+v", res, *sent)
	}
	for _, e := range *sent {
		if e.from != "192.0.2.10" || e.to != "192.0.2.255" {
			t.Fatalf("saiu por %+v", e)
		}
	}
	// Sem rede pedida: todas as redes com broadcast (a /32 da VPN fica de fora).
	s, sent = fake(ifaces, nil)
	if res, err = s.Wake(context.Background(), macs[:1], netip.Prefix{}); err != nil || len(res.Networks) != 2 || len(*sent) != 2*Repeats {
		t.Fatalf("%+v %v", res, err)
	}
	// A rede pedida não é desta máquina: recusa, nada sai.
	s, sent = fake(ifaces, nil)
	if _, err = s.Wake(context.Background(), macs, netip.MustParsePrefix("203.0.113.0/24")); !errors.Is(err, ErrNoNetwork) || len(*sent) != 0 {
		t.Fatalf("rede alheia: %v, %d enviados", err, len(*sent))
	}
	// Uma rede falha, a outra vai: conta como enviado.
	s, _ = fake(ifaces, map[string]bool{"198.51.100.255": true})
	if res, err = s.Wake(context.Background(), macs[:1], netip.Prefix{}); err != nil || res.Sent != Repeats {
		t.Fatalf("%+v %v", res, err)
	}
	// Tudo falha: erro.
	s, _ = fake(ifaces, map[string]bool{"198.51.100.255": true, "192.0.2.255": true})
	if _, err = s.Wake(context.Background(), macs[:1], netip.Prefix{}); err == nil {
		t.Fatal("nada mandado e sem erro")
	}
	if _, err = s.Wake(context.Background(), nil, netip.Prefix{}); err == nil {
		t.Fatal("sem MAC aceito")
	}
}

// O envio de verdade, pela loopback: o datagrama sai e tem o formato certo.
func TestSendUDP(t *testing.T) {
	ln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	port := ln.LocalAddr().(*net.UDPAddr).Port
	mac, _ := ParseMAC("00:11:22:33:44:55")
	d := net.Dialer{}
	c, err := d.DialContext(context.Background(), "udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write(Packet(mac))
	c.Close()
	buf := make([]byte, 200)
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFromUDP(buf)
	if err != nil || n != 102 {
		t.Fatalf("%d bytes, %v", n, err)
	}
	// O sendUDP abre e fecha sem erro para um destino local.
	if err := sendUDP(context.Background(), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), Packet(mac)); err != nil {
		t.Fatal(err)
	}
}
