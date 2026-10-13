// Package hoststate coleta o estado volátil da máquina (Aula 12.1): interfaces de rede e
// espaço nos volumes locais. Diferente do inventário, não tem hash nem histórico: vai em
// todo check-in, e o servidor guarda só o último.
package hoststate

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Collect lê a rede e os discos. Uma parte que falha vai para Errors; a outra segue.
func Collect(ctx context.Context) protocol.MachineState {
	s := protocol.MachineState{CollectedAt: time.Now().UTC(), Interfaces: []protocol.NetInterface{}, Disks: []protocol.Disk{}}
	ifs, err := interfaces()
	if err != nil {
		s.Errors = append(s.Errors, "rede: "+err.Error())
	}
	s.Interfaces = append(s.Interfaces, ifs...)
	ds, errs := disks(ctx)
	s.Disks = append(s.Disks, ds...)
	for _, e := range errs {
		s.Errors = append(s.Errors, "discos: "+e.Error())
	}
	trim(&s)
	return s
}

// trim corta o estado nos limites do protocolo: uma máquina com centenas de veth (um host
// de containers) não pode ter o check-in recusado por isso.
func trim(s *protocol.MachineState) {
	if len(s.Interfaces) > protocol.MaxStateInterfaces {
		s.Interfaces = s.Interfaces[:protocol.MaxStateInterfaces]
	}
	for i := range s.Interfaces {
		n := &s.Interfaces[i]
		if len(n.IPv4) > protocol.MaxStateAddrs {
			n.IPv4 = n.IPv4[:protocol.MaxStateAddrs]
		}
		if len(n.IPv6) > protocol.MaxStateAddrs {
			n.IPv6 = n.IPv6[:protocol.MaxStateAddrs]
		}
	}
	if len(s.Disks) > protocol.MaxStateDisks {
		s.Disks = s.Disks[:protocol.MaxStateDisks]
	}
	if len(s.Errors) > protocol.MaxStateErrors {
		s.Errors = s.Errors[:protocol.MaxStateErrors]
	}
}

// netIf é o que o Go diz de uma interface, já com os endereços: separado de net.Interface
// para o filtro ser testado com dados montados à mão.
type netIf struct {
	name  string
	mac   net.HardwareAddr
	flags net.Flags
	addrs []netip.Prefix
}

func interfaces() ([]protocol.NetInterface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	raw := make([]netIf, 0, len(list))
	for _, i := range list {
		n := netIf{name: i.Name, mac: i.HardwareAddr, flags: i.Flags}
		addrs, err := i.Addrs()
		if err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					if p, ok := prefixOf(ipn); ok {
						n.addrs = append(n.addrs, p)
					}
				}
			}
		}
		raw = append(raw, n)
	}
	return selectInterfaces(raw), nil
}

func prefixOf(ipn *net.IPNet) (netip.Prefix, bool) {
	ip, ok := netip.AddrFromSlice(ipn.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := ipn.Mask.Size()
	return netip.PrefixFrom(ip.Unmap(), ones), true
}

// selectInterfaces fica com o que interessa a quem procura a máquina:
//   - sem a loopback;
//   - IPv6 só os globais (os fe80:: existem em toda placa e não identificam nada);
//   - sem as interfaces com MAC local e nenhum endereço (os veth de cada container);
//   - sem as que não têm MAC nem endereço (túneis parados, adaptadores ocultos do Windows).
//
// As ativas vêm primeiro, depois em ordem de nome.
func selectInterfaces(raw []netIf) []protocol.NetInterface {
	out := []protocol.NetInterface{}
	for _, r := range raw {
		if r.flags&net.FlagLoopback != 0 {
			continue
		}
		n := protocol.NetInterface{Name: r.name, Up: r.flags&net.FlagUp != 0, LocalMAC: protocol.IsLocalMAC(r.mac)}
		if len(r.mac) > 0 {
			n.MAC = r.mac.String()
		}
		for _, p := range r.addrs {
			ip := p.Addr()
			switch {
			case ip.IsLoopback():
			case ip.Is4():
				n.IPv4 = append(n.IPv4, p.String())
			case ip.IsGlobalUnicast(): // inclui os ULA (fc00::/7), da rede interna
				n.IPv6 = append(n.IPv6, p.String())
			}
		}
		noAddr := len(n.IPv4) == 0 && len(n.IPv6) == 0
		if noAddr && (n.MAC == "" || n.LocalMAC) {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Up != out[j].Up {
			return out[i].Up
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// dedupe fica com um ponto de montagem por dispositivo (o de caminho mais curto): bind
// mounts e subvolumes do btrfs mostram o mesmo espaço várias vezes. Ordena por montagem.
func dedupe(ds []protocol.Disk) []protocol.Disk {
	byDev := map[string]int{}
	out := []protocol.Disk{}
	for _, d := range ds {
		if d.Device == "" {
			out = append(out, d)
			continue
		}
		if i, ok := byDev[d.Device]; ok {
			if len(d.Mount) < len(out[i].Mount) {
				out[i] = d
			}
			continue
		}
		byDev[d.Device] = len(out)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}
