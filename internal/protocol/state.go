package protocol

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// MachineState é o estado volátil da máquina (Aula 12.1): interfaces de rede e espaço em
// disco. Vai em todo check-in, e o servidor guarda só o último.
//
// Fica fora do inventário de propósito. O inventário é versionado pelo hash: um conteúdo
// novo vira uma linha nova em inventory_reports, guardada com histórico. O espaço livre
// muda a cada hora e o IP muda a cada DHCP; dentro do hash, cada check-in viraria um
// inventário novo, e a tabela que a retenção segura voltaria a crescer sem parar.
type MachineState struct {
	CollectedAt time.Time      `json:"collected_at"`
	Interfaces  []NetInterface `json:"interfaces"`
	Disks       []Disk         `json:"disks"`
	Errors      []string       `json:"errors,omitempty"` // falhas parciais da coleta
}

// NetInterface é uma interface de rede (sem a loopback).
type NetInterface struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`  // aa:bb:cc:dd:ee:ff
	IPv4 []string `json:"ipv4,omitempty"` // com o prefixo: 192.0.2.10/24
	IPv6 []string `json:"ipv6,omitempty"` // só os globais (os fe80:: de enlace ficam de fora)
	Up   bool     `json:"up"`
	// LocalMAC: MAC administrado localmente (bit 0x02 do primeiro byte). É o caso de
	// bridges, veth e VPNs, e do Wi-Fi com endereço aleatório. A placa de uma VM (Hyper-V,
	// VMware) usa MAC de fabricante e não é marcada: de dentro da VM, ela é a placa.
	LocalMAC bool `json:"local_mac,omitempty"`
}

// Disk é um volume local: partição montada (Linux, macOS) ou unidade fixa (Windows).
type Disk struct {
	Mount      string `json:"mount"`            // "/", "/home", "C:"
	FS         string `json:"fs,omitempty"`     // ext4, xfs, apfs, NTFS
	Device     string `json:"device,omitempty"` // /dev/sda2 (Linux e macOS)
	Model      string `json:"model,omitempty"`  // modelo do disco (Windows, Aula 12.1d)
	Bus        string `json:"bus,omitempty"`    // barramento: NVMe, SATA, USB... (Windows)
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"` // disponível para um usuário comum
}

// Limites do estado. O agente corta antes de enviar; o servidor recusa o que passar.
const (
	MaxStateInterfaces = 64
	MaxStateAddrs      = 32 // por família, por interface
	MaxStateDisks      = 64
	MaxStateErrors     = 16
	maxStateText       = 256
)

// IsLocalMAC diz se o MAC é administrado localmente.
func IsLocalMAC(mac net.HardwareAddr) bool { return len(mac) > 0 && mac[0]&0x02 != 0 }

// Check confere o estado recebido: tamanhos, MACs, endereços e espaço. O servidor descarta
// o estado inteiro (e mantém o check-in) quando ele não passa.
func (s MachineState) Check() error {
	switch {
	case s.CollectedAt.IsZero():
		return errors.New("collected_at ausente")
	case len(s.Interfaces) > MaxStateInterfaces:
		return fmt.Errorf("%d interfaces (máximo %d)", len(s.Interfaces), MaxStateInterfaces)
	case len(s.Disks) > MaxStateDisks:
		return fmt.Errorf("%d discos (máximo %d)", len(s.Disks), MaxStateDisks)
	case len(s.Errors) > MaxStateErrors:
		return fmt.Errorf("%d erros (máximo %d)", len(s.Errors), MaxStateErrors)
	}
	text := func(campo, v string, vazioOK bool) error {
		switch {
		case v == "" && !vazioOK:
			return fmt.Errorf("%s vazio", campo)
		case len(v) > maxStateText:
			return fmt.Errorf("%s com mais de %d bytes", campo, maxStateText)
		case strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return fmt.Errorf("%s com caractere de controle", campo)
		}
		return nil
	}
	for i, n := range s.Interfaces {
		if err := text("nome da interface", n.Name, false); err != nil {
			return fmt.Errorf("interface %d: %w", i, err)
		}
		if n.MAC != "" {
			if _, err := net.ParseMAC(n.MAC); err != nil {
				return fmt.Errorf("interface %d: MAC inválido", i)
			}
		}
		if len(n.IPv4) > MaxStateAddrs || len(n.IPv6) > MaxStateAddrs {
			return fmt.Errorf("interface %d: endereços demais", i)
		}
		for _, a := range n.IPv4 {
			if p, err := netip.ParsePrefix(a); err != nil || !p.Addr().Is4() {
				return fmt.Errorf("interface %d: IPv4 inválido %q", i, a)
			}
		}
		for _, a := range n.IPv6 {
			if p, err := netip.ParsePrefix(a); err != nil || !p.Addr().Is6() || p.Addr().Is4In6() {
				return fmt.Errorf("interface %d: IPv6 inválido %q", i, a)
			}
		}
	}
	for i, d := range s.Disks {
		if err := text("ponto de montagem", d.Mount, false); err != nil {
			return fmt.Errorf("disco %d: %w", i, err)
		}
		if err := text("sistema de arquivos", d.FS, true); err != nil {
			return fmt.Errorf("disco %d: %w", i, err)
		}
		if err := text("dispositivo", d.Device, true); err != nil {
			return fmt.Errorf("disco %d: %w", i, err)
		}
		if err := text("modelo", d.Model, true); err != nil {
			return fmt.Errorf("disco %d: %w", i, err)
		}
		if err := text("barramento", d.Bus, true); err != nil {
			return fmt.Errorf("disco %d: %w", i, err)
		}
		if d.FreeBytes > d.TotalBytes {
			return fmt.Errorf("disco %d: livre (%d) maior que o total (%d)", i, d.FreeBytes, d.TotalBytes)
		}
	}
	for i, e := range s.Errors {
		if err := text("erro", e, false); err != nil {
			return fmt.Errorf("erro %d: %w", i, err)
		}
	}
	return nil
}

// PrimaryIPv4 escolhe o IPv4 que representa a máquina nas listas (sem o prefixo).
//
//  1. O endereço de onde veio o check-in, se a máquina o tem numa interface: é por ele que
//     ela fala com o servidor.
//  2. Senão (NAT, proxy, VPN no caminho), o primeiro IPv4 de uma interface ativa com MAC
//     de fabricante, fora do 169.254/16 (sem DHCP).
//  3. Senão, o primeiro IPv4 de uma interface ativa.
//
// "" quando a máquina não tem IPv4.
func (s MachineState) PrimaryIPv4(checkinAddr string) string {
	seen, _ := netip.ParseAddr(checkinAddr)
	seen = seen.Unmap()
	var fallback, best string
	for _, n := range s.Interfaces {
		for _, a := range n.IPv4 {
			p, err := netip.ParsePrefix(a)
			if err != nil {
				continue
			}
			ip := p.Addr()
			if seen.IsValid() && ip == seen {
				return ip.String()
			}
			if !n.Up || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
				continue
			}
			if fallback == "" {
				fallback = ip.String()
			}
			if best == "" && !n.LocalMAC {
				best = ip.String()
			}
		}
	}
	if best != "" {
		return best
	}
	return fallback
}

// HasAddr diz se o IP (sem prefixo) está em alguma interface da máquina.
func (s MachineState) HasAddr(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, n := range s.Interfaces {
		for _, a := range append(append([]string{}, n.IPv4...), n.IPv6...) {
			if p, err := netip.ParsePrefix(a); err == nil && p.Addr() == ip {
				return true
			}
		}
	}
	return false
}
