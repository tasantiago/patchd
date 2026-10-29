// Package descoberta varre faixas de IP atrás de máquinas vivas na rede e monta o comando de
// instalação do agente para cada uma (Aula 23.7).
//
// A varredura é uma sondagem TCP: para cada endereço da faixa, o servidor tenta abrir a
// conexão em algumas portas (445/3389 do Windows, 22 do SSH). Quem responde em alguma está
// vivo. Não há ICMP (ping): o servidor roda num container sem root, que não abre soquete
// bruto, e muita máquina ignora o ping mas atende essas portas. A varredura só alcança as
// sub-redes que a VM do servidor enxerga.
//
// O palpite de SO sai das portas abertas, e com ele o painel mostra o comando certo
// (PowerShell/PsExec para Windows, SSH para Linux). A instalação automática Windows é
// uma operação tipada separada dos comandos manuais.
package descoberta

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Portas sondadas, na ordem. 445 e 3389 marcam Windows; 22, SSH.
var DefaultPorts = []int{445, 3389, 22, 5986}

// Limites da varredura.
const (
	MaxSpecHosts = 1 << 16 // uma faixa não passa de um /16 (65 536 endereços)
	MaxScanHosts = 1 << 14 // uma varredura sonda no máximo 16 384 endereços; o resto fica para a próxima
	MaxRanges    = 64      // faixas cadastradas
	maxNote      = 120
)

// Spec é uma faixa de busca já conferida: um CIDR (10.0.0.0/24) ou um intervalo
// (10.0.0.10-10.0.0.60, ou 10.0.0.10-60 pelo último octeto). Só IPv4.
type Spec struct {
	Text     string // como foi cadastrada, normalizada
	from, to netip.Addr
}

// Contains checks membership without expanding a range.
func (s Spec) Contains(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && a.Is4() && !a.Less(s.from) && !s.to.Less(a)
}

// ParseSpec confere uma faixa e devolve a Spec normalizada.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, fmt.Errorf("faixa vazia")
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return Spec{}, fmt.Errorf("CIDR inválido: %s", s)
		}
		p = p.Masked()
		if !p.Addr().Is4() {
			return Spec{}, fmt.Errorf("só faixas IPv4: %s", s)
		}
		from := p.Addr()
		to := lastInPrefix(p)
		sp := Spec{Text: p.String(), from: from, to: to}
		if sp.Count() > MaxSpecHosts {
			return Spec{}, fmt.Errorf("faixa grande demais (%s tem mais de %d endereços; divida em sub-redes menores)", s, MaxSpecHosts)
		}
		return sp, nil
	}
	ini, fim, ok := strings.Cut(s, "-")
	if !ok {
		// um IP solto vira uma faixa de um endereço
		a, err := parse4(s)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Text: a.String(), from: a, to: a}, nil
	}
	from, err := parse4(ini)
	if err != nil {
		return Spec{}, err
	}
	fim = strings.TrimSpace(fim)
	var to netip.Addr
	if strings.Contains(fim, ".") {
		if to, err = parse4(fim); err != nil {
			return Spec{}, err
		}
	} else {
		// atalho pelo último octeto: 10.0.0.10-60
		to, err = lastOctet(from, fim)
		if err != nil {
			return Spec{}, err
		}
	}
	if to.Less(from) {
		return Spec{}, fmt.Errorf("faixa ao contrário: %s", s)
	}
	sp := Spec{Text: from.String() + "-" + to.String(), from: from, to: to}
	if sp.Count() > MaxSpecHosts {
		return Spec{}, fmt.Errorf("faixa grande demais (%s tem mais de %d endereços)", s, MaxSpecHosts)
	}
	return sp, nil
}

func parse4(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("endereço IPv4 inválido: %s", strings.TrimSpace(s))
	}
	return a, nil
}

func lastOctet(from netip.Addr, oct string) (netip.Addr, error) {
	b := from.As4()
	var n int
	if _, err := fmt.Sscanf(oct, "%d", &n); err != nil || n < 0 || n > 255 || n < int(b[3]) {
		return netip.Addr{}, fmt.Errorf("fim do intervalo inválido: %s", oct)
	}
	b[3] = byte(n)
	return netip.AddrFrom4(b), nil
}

func lastInPrefix(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	bits := p.Bits()
	for i := 0; i < 4; i++ {
		maskBits := bits - 8*i
		switch {
		case maskBits >= 8:
			// octeto inteiro na rede: sem host
		case maskBits <= 0:
			b[i] |= 0xff
		default:
			b[i] |= 0xff >> maskBits
		}
	}
	return netip.AddrFrom4(b)
}

// Count é quantos endereços a faixa tem (inclusive as pontas).
func (s Spec) Count() uint64 {
	if !s.from.IsValid() {
		return 0
	}
	return addrToU32(s.to) - addrToU32(s.from) + 1
}

func addrToU32(a netip.Addr) uint64 {
	b := a.As4()
	return uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
}

func u32ToAddr(u uint64) netip.Addr {
	// u guarda um IPv4 de 32 bits; cada octeto é mascarado em 8 bits.
	return netip.AddrFrom4([4]byte{ // #nosec G115 -- octetos mascarados (& 0xff)
		byte(u >> 24 & 0xff), byte(u >> 16 & 0xff), byte(u >> 8 & 0xff), byte(u & 0xff)})
}

// Finding é uma máquina viva: o IP e as portas abertas, com o palpite de SO.
type Finding struct {
	IP    netip.Addr
	Ports []int
	OS    string // "windows" | "ssh" | ""
}

// Dial abre uma conexão TCP e diz se o host atendeu naquela porta. O servidor injeta o seu;
// o teste injeta um falso.
type Dial func(ctx context.Context, ip netip.Addr, port int) bool

// Options reúne o que a varredura usa de fora.
type Options struct {
	Ports       []int         // nil: DefaultPorts
	Concurrency int           // 0: 512
	Dial        Dial          // obrigatório
	Progress    *atomic.Int64 // opcional: +1 a cada endereço sondado, para a página do painel
}

// Scan sonda as faixas e devolve as máquinas vivas, em ordem de IP, mais quantos endereços
// chegaram a ser sondados (pode ser menos que o total: o teto é MaxScanHosts) e se houve
// corte. As faixas se sobrepondo, cada endereço é sondado uma vez só.
func Scan(ctx context.Context, specs []Spec, o Options) (found []Finding, scanned int, partial bool) {
	ports := o.Ports
	if ports == nil {
		ports = DefaultPorts
	}
	conc := o.Concurrency
	if conc <= 0 {
		conc = 512
	}
	seen := map[uint64]bool{}
	var queue []netip.Addr
	for _, sp := range specs {
		for u := addrToU32(sp.from); u <= addrToU32(sp.to); u++ {
			if seen[u] {
				continue
			}
			seen[u] = true
			if len(queue) >= MaxScanHosts {
				partial = true
				break
			}
			queue = append(queue, u32ToAddr(u))
		}
		if partial {
			break
		}
	}
	scanned = len(queue)

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, conc)
	)
	for _, ip := range queue {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip netip.Addr) {
			defer wg.Done()
			defer func() { <-sem }()
			if o.Progress != nil {
				defer o.Progress.Add(1)
			}
			var open []int
			for _, p := range ports {
				if ctx.Err() != nil {
					return
				}
				if o.Dial(ctx, ip, p) {
					open = append(open, p)
				}
			}
			if len(open) == 0 {
				return
			}
			mu.Lock()
			found = append(found, Finding{IP: ip, Ports: open, OS: osGuess(open)})
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].IP.Less(found[j].IP) })
	return found, scanned, partial
}

func osGuess(ports []int) string {
	has := func(p int) bool {
		for _, x := range ports {
			if x == p {
				return true
			}
		}
		return false
	}
	switch {
	case has(445) || has(3389) || has(135) || has(5986):
		return "windows"
	case has(22):
		return "ssh"
	}
	return ""
}

// PortsText escreve as portas como "445, 3389".
func PortsText(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprintf("%d", p)
	}
	return strings.Join(s, ", ")
}
