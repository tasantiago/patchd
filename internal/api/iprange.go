package api

import (
	"net/netip"
	"strconv"
	"strings"
)

// Faixa de IP na busca do Inventário: a mesma caixa da busca por texto aceita uma faixa, e aí
// a máquina entra se algum IPv4 dela (das placas ou o do check-in) estiver dentro.

// ipRange é uma faixa de IPv4, inclusiva.
type ipRange struct{ lo, hi netip.Addr }

func (r ipRange) contains(a netip.Addr) bool {
	a = a.Unmap()
	return a.Is4() && r.lo.Compare(a) <= 0 && a.Compare(r.hi) <= 0
}

// parseIPRange lê as formas aceitas:
//
//	10.1.2.0/24            rede (CIDR)
//	10.1.2.10-10.1.2.50    do primeiro ao último
//	10.1.2.10-50           atalho: o fim muda só o último número
//	10.1.2.*  ou 10.1.*    curinga no fim (o * vale de 0 a 255)
//
// Um IP sozinho não é faixa: continua a busca por texto (que já acha o IP exato).
func parseIPRange(s string) (ipRange, bool) {
	s = strings.TrimSpace(s)
	switch {
	case strings.Contains(s, "/"):
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			return ipRange{}, false
		}
		p = p.Masked()
		return ipRange{lo: p.Addr(), hi: lastOf(p)}, true
	case strings.Contains(s, "-"):
		a, b, _ := strings.Cut(s, "-")
		lo, err := netip.ParseAddr(strings.TrimSpace(a))
		if err != nil || !lo.Is4() {
			return ipRange{}, false
		}
		b = strings.TrimSpace(b)
		hi, err := netip.ParseAddr(b)
		if err != nil {
			n, err := strconv.Atoi(b)
			if err != nil || n < 0 || n > 255 {
				return ipRange{}, false
			}
			v := lo.As4()
			v[3] = byte(n)
			hi = netip.AddrFrom4(v)
		}
		if !hi.Is4() || hi.Compare(lo) < 0 {
			return ipRange{}, false
		}
		return ipRange{lo: lo, hi: hi}, true
	case strings.HasSuffix(s, ".*"):
		parts := strings.Split(strings.TrimSuffix(s, ".*"), ".")
		if len(parts) < 1 || len(parts) > 3 {
			return ipRange{}, false
		}
		var v [4]byte
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 || p == "" {
				return ipRange{}, false
			}
			v[i] = byte(n)
		}
		lo := netip.AddrFrom4(v)
		return ipRange{lo: lo, hi: lastOf(netip.PrefixFrom(lo, 8*len(parts)))}, true
	}
	return ipRange{}, false
}

// lastOf: o último endereço da rede.
func lastOf(p netip.Prefix) netip.Addr {
	v := p.Masked().Addr().As4()
	n := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
	n |= uint32(1)<<(32-p.Bits()) - 1
	if p.Bits() == 0 {
		n = 0xffffffff
	}
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
