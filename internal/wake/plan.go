package wake

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wol"
)

// O pedido de ligar (Aula 22.2): o servidor não manda o pacote (ele não está na rede das
// máquinas, e o pacote não passa pelo roteador). Escolhe agentes ligados da MESMA sub-rede
// da máquina desligada, os repetidores, e manda a cada um um job assinado com os MACs dela.

// Limites da escolha.
const (
	// MaxRelays: os repetidores de um pedido. Mais de um encurta a espera (o job sai no
	// próximo check-in de cada um, que é de hora em hora) e cobre o que desligar no meio.
	MaxRelays = 3
	// RelayFresh: só repete quem fez check-in há pouco (o intervalo é de 1 hora).
	RelayFresh = 2 * time.Hour
)

// Host é uma máquina candidata a repetidor.
type Host struct {
	ID, Name string
	LastSeen time.Time
	State    *protocol.MachineState
}

// Plan é o pedido montado.
type Plan struct {
	MACs    []string
	Network netip.Prefix
	Relays  []Host
}

// Erros da montagem (o texto vai para a tela).
var (
	ErrNoMAC     = errors.New("o patchd não conhece o MAC de uma placa com cabo desta máquina (o agente ainda não mandou a rede e o Wake on LAN)")
	ErrNoNetwork = errors.New("o patchd não conhece a sub-rede desta máquina (o agente ainda não mandou a rede)")
)

// NoRelayError: nenhuma máquina ligada na mesma sub-rede.
type NoRelayError struct{ Networks []netip.Prefix }

func (e NoRelayError) Error() string {
	return fmt.Sprintf("nenhuma máquina com agente atualizado fez check-in nas últimas %d horas em %v: o pacote não passa pelo roteador e precisa sair de dentro da sub-rede",
		int(RelayFresh.Hours()), e.Networks)
}

// MACs: as placas com cabo que o agente informou (Aula 22.1), menos as sem Wake on Magic
// Packet; sem o bloco wake, as placas com MAC de fabricante e IPv4 da última rede.
func MACs(w *protocol.Wake, st *protocol.MachineState) []string {
	var out []string
	seen := map[string]bool{}
	add := func(m string) {
		hw, err := wol.ParseMAC(m)
		if err != nil || seen[hw.String()] || len(out) >= wol.MaxMACs {
			return
		}
		seen[hw.String()] = true
		out = append(out, hw.String())
	}
	if w != nil {
		for _, a := range w.Adapters {
			if a.Wired && a.MagicPacket != protocol.WakeOff && a.MagicPacket != protocol.WakeUnsupported {
				add(a.MAC)
			}
		}
	}
	if len(out) == 0 && st != nil {
		for _, n := range st.Interfaces {
			if !n.LocalMAC && len(n.IPv4) > 0 {
				add(n.MAC)
			}
		}
	}
	return out
}

// Networks: as sub-redes IPv4 da máquina no último estado (sem as de MAC local, como VPN e
// bridge, e sem o 169.254/16), na forma de rede.
func Networks(st *protocol.MachineState) []netip.Prefix {
	if st == nil {
		return nil
	}
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, n := range st.Interfaces {
		if n.LocalMAC {
			continue
		}
		for _, a := range n.IPv4 {
			p, err := netip.ParsePrefix(a)
			if err != nil || !p.Addr().Is4() || p.Addr().IsLinkLocalUnicast() || p.Addr().IsLoopback() || p.Bits() < 8 || p.Bits() > 30 {
				continue
			}
			m := p.Masked()
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// inNetwork: a máquina tem um IPv4 na rede (com o mesmo prefixo).
func inNetwork(st *protocol.MachineState, network netip.Prefix) bool {
	if st == nil {
		return false
	}
	for _, n := range st.Interfaces {
		if !n.Up {
			continue
		}
		for _, a := range n.IPv4 {
			if p, err := netip.ParsePrefix(a); err == nil && p.Masked() == network {
				return true
			}
		}
	}
	return false
}

// Build monta o pedido: os MACs, a sub-rede e até MaxRelays repetidores (os de check-in mais
// recente), na primeira sub-rede da máquina que tem algum. candidates não deve ter a própria
// máquina.
func Build(w *protocol.Wake, st *protocol.MachineState, candidates []Host, now time.Time) (Plan, error) {
	var p Plan
	if p.MACs = MACs(w, st); len(p.MACs) == 0 {
		return p, ErrNoMAC
	}
	nets := Networks(st)
	if len(nets) == 0 {
		return p, ErrNoNetwork
	}
	fresh := make([]Host, 0, len(candidates))
	for _, c := range candidates {
		if c.State != nil && c.State.WakeRelay && now.Sub(c.LastSeen) <= RelayFresh {
			fresh = append(fresh, c)
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].LastSeen.After(fresh[j].LastSeen) })
	for _, n := range nets {
		var relays []Host
		for _, c := range fresh {
			if inNetwork(c.State, n) {
				relays = append(relays, c)
				if len(relays) == MaxRelays {
					break
				}
			}
		}
		if len(relays) > 0 {
			p.Network, p.Relays = n, relays
			return p, nil
		}
	}
	return p, NoRelayError{Networks: nets}
}
