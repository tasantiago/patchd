// Package wol manda o pacote mágico do Wake on LAN (docs/wake-on-lan.md, Aula 22.2).
//
// O pacote é 6 bytes 0xFF seguidos de 16 cópias do MAC da máquina a acordar, num datagrama
// UDP para o broadcast da sub-rede, porta 9 (discard). Ele não passa pelo roteador: quem
// manda é um agente ligado na mesma sub-rede da máquina desligada.
//
// O que o pacote NÃO faz, de propósito: não vai para endereço escolhido de fora (só o
// broadcast das redes da própria máquina), não varre portas e não leva outra coisa além do
// MAC. O agente não vira ferramenta de varredura nem de inundação.
package wol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Port é a porta do pacote (9, discard: ninguém escuta, a placa desligada olha o conteúdo).
const Port = 9

// Repeats: cada pacote vai algumas vezes (UDP sem confirmação; um switch pode perder um).
const Repeats = 3

// repeatGap: o intervalo entre as repetições.
const repeatGap = 100 * time.Millisecond

// MaxMACs: os MACs de um job (uma máquina tem poucas placas com cabo; o job é de uma máquina).
const MaxMACs = 8

// ParseMAC lê um MAC Ethernet (aa:bb:cc:dd:ee:ff ou com hífens) e confere que é de uma placa:
// nem zero, nem broadcast, nem multicast.
func ParseMAC(s string) (net.HardwareAddr, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("MAC inválido: %q", s)
	}
	switch {
	case bytes.Equal(hw, make([]byte, 6)):
		return nil, fmt.Errorf("MAC zerado: %q", s)
	case hw[0]&0x01 != 0:
		return nil, fmt.Errorf("MAC de grupo (multicast ou broadcast), não de uma placa: %q", s)
	}
	return hw, nil
}

// Packet monta o pacote mágico de um MAC (102 bytes).
func Packet(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 6+16*6)
	p = append(p, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	for range 16 {
		p = append(p, mac...)
	}
	return p
}

// Iface é uma rede IPv4 de uma interface da máquina que manda.
type Iface struct {
	Name   string
	Prefix netip.Prefix // o endereço da máquina com o prefixo: 192.0.2.10/24
}

// Broadcast: o último endereço da rede (192.0.2.255 em 192.0.2.10/24). false em /31 e /32
// (sem broadcast) e fora do IPv4.
func (i Iface) Broadcast() (netip.Addr, bool) {
	p := i.Prefix
	if !p.Addr().Is4() || p.Bits() > 30 || p.Bits() < 8 {
		return netip.Addr{}, false
	}
	b := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	n |= host
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true
}

// Sender manda os datagramas. O padrão usa a rede de verdade; os testes trocam.
type Sender struct {
	// Ifaces lista as redes IPv4 das interfaces ligadas (sem a loopback).
	Ifaces func() ([]Iface, error)
	// Send manda um datagrama de from para to:Port.
	Send  func(ctx context.Context, from, to netip.Addr, payload []byte) error
	Sleep func(ctx context.Context, d time.Duration) error
}

// Default é o Sender da rede de verdade.
func Default() *Sender {
	return &Sender{Ifaces: systemIfaces, Send: sendUDP, Sleep: sleep}
}

// Result diz por onde os pacotes foram.
type Result struct {
	Networks []string // as redes usadas: "eth0 192.0.2.0/24 → 192.0.2.255"
	Sent     int      // datagramas mandados
}

// ErrNoNetwork: a máquina não tem a rede pedida (ou nenhuma rede com broadcast).
var ErrNoNetwork = errors.New("nenhuma rede local com broadcast para mandar o pacote")

// Wake manda o pacote de cada MAC pelo broadcast das redes da máquina. network (válida)
// restringe às interfaces dessa rede; inválida: todas. O destino nunca vem de fora: é o
// broadcast calculado aqui, das redes desta máquina.
func (s *Sender) Wake(ctx context.Context, macs []net.HardwareAddr, network netip.Prefix) (Result, error) {
	var res Result
	if len(macs) == 0 || len(macs) > MaxMACs {
		return res, fmt.Errorf("de 1 a %d MACs", MaxMACs)
	}
	ifaces, err := s.Ifaces()
	if err != nil {
		return res, fmt.Errorf("listar as interfaces: %w", err)
	}
	type route struct {
		from, to netip.Addr
		label    string
	}
	var routes []route
	seen := map[netip.Addr]bool{}
	for _, i := range ifaces {
		bc, ok := i.Broadcast()
		if !ok || seen[bc] {
			continue
		}
		if network.IsValid() && i.Prefix.Masked() != network.Masked() {
			continue
		}
		seen[bc] = true
		routes = append(routes, route{from: i.Prefix.Addr(), to: bc, label: fmt.Sprintf("%s %s → %s", i.Name, i.Prefix.Masked(), bc)})
	}
	if len(routes) == 0 {
		if network.IsValid() {
			return res, fmt.Errorf("%w: esta máquina não está em %s", ErrNoNetwork, network.Masked())
		}
		return res, ErrNoNetwork
	}
	var errs []error
	for rep := range Repeats {
		if rep > 0 {
			if err := s.Sleep(ctx, repeatGap); err != nil {
				return res, err
			}
		}
		for _, r := range routes {
			for _, mac := range macs {
				if err := s.Send(ctx, r.from, r.to, Packet(mac)); err != nil {
					if rep == 0 {
						errs = append(errs, fmt.Errorf("%s: %w", r.label, err))
					}
					continue
				}
				res.Sent++
			}
		}
	}
	for _, r := range routes {
		res.Networks = append(res.Networks, r.label)
	}
	if res.Sent == 0 {
		return res, errors.Join(errs...)
	}
	return res, nil
}

func systemIfaces() ([]Iface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Iface
	for _, ni := range list {
		if ni.Flags&net.FlagUp == 0 || ni.Flags&net.FlagLoopback != 0 || ni.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := ni.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, _ := ipn.Mask.Size()
			if !ip.Is4() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, Iface{Name: ni.Name, Prefix: netip.PrefixFrom(ip, ones)})
		}
	}
	return out, nil
}

// sendUDP manda do endereço da interface (a rota sai pela placa certa) para o broadcast.
// O Go liga SO_BROADCAST nos sockets UDP.
func sendUDP(ctx context.Context, from, to netip.Addr, payload []byte) error {
	d := net.Dialer{LocalAddr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(from, 0)), Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "udp4", netip.AddrPortFrom(to, Port).String())
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err = c.Write(payload)
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
