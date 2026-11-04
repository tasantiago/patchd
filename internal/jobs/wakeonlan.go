package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/tasantiago/patchd/internal/wol"
)

// TypeWakeOnLAN (docs/wake-on-lan.md, Aula 22.2) pede a um agente LIGADO que mande o pacote
// mágico para acordar OUTRA máquina da mesma sub-rede. O job vai para o repetidor, não para a
// máquina desligada (que não faz check-in). A lista de MACs é fechada e pequena, o destino é
// só o broadcast das redes do próprio repetidor (calculado por ele, nunca vindo do servidor),
// e a porta é fixa (9): o job não vira varredura nem inundação.
const TypeWakeOnLAN = "wake_on_lan"

// WakeOnLANParams são os parâmetros do job.
type WakeOnLANParams struct {
	// Target: a máquina a acordar (o ID do patchd), para o log do repetidor e para o servidor
	// casar o resultado com o pedido.
	Target string `json:"target"`
	// MACs: as placas com cabo da máquina, na forma normal (aa:bb:cc:dd:ee:ff).
	MACs []string `json:"macs"`
	// Network: a rede da máquina (192.0.2.0/24). O repetidor só manda pelas interfaces dele
	// nessa rede; se ele já não estiver nela, recusa.
	Network string `json:"network"`
}

// ParseWakeOnLANParams lê e confere os parâmetros.
func ParseWakeOnLANParams(raw json.RawMessage) (WakeOnLANParams, error) {
	var p WakeOnLANParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do wake_on_lan ilegíveis: %w", err)
	}
	if p.Target == "" || len(p.Target) > 64 {
		return p, fmt.Errorf("máquina a acordar ausente ou longa demais")
	}
	if len(p.MACs) == 0 || len(p.MACs) > wol.MaxMACs {
		return p, fmt.Errorf("de 1 a %d MACs", wol.MaxMACs)
	}
	seen := map[string]bool{}
	for _, m := range p.MACs {
		hw, err := wol.ParseMAC(m)
		if err != nil {
			return p, err
		}
		if hw.String() != m {
			return p, fmt.Errorf("MAC fora da forma normal: %q (use %q)", m, hw.String())
		}
		if seen[m] {
			return p, fmt.Errorf("MAC repetido: %s", m)
		}
		seen[m] = true
	}
	if _, err := p.Prefix(); err != nil {
		return p, err
	}
	return p, nil
}

// Prefix devolve a rede, conferida: IPv4, na forma de rede (sem bits de host), de /8 a /30.
func (p WakeOnLANParams) Prefix() (netip.Prefix, error) {
	n, err := netip.ParsePrefix(p.Network)
	switch {
	case err != nil || !n.Addr().Is4():
		return netip.Prefix{}, fmt.Errorf("rede inválida: %q (IPv4 com prefixo, ex.: 192.0.2.0/24)", p.Network)
	case n != n.Masked():
		return netip.Prefix{}, fmt.Errorf("rede com bits de máquina: %q (use %s)", p.Network, n.Masked())
	case n.Bits() < 8 || n.Bits() > 30:
		return netip.Prefix{}, fmt.Errorf("rede de /8 a /30 (recebido /%d)", n.Bits())
	}
	return n, nil
}

// WakeOnLANParamsJSON monta os parâmetros conferidos.
func WakeOnLANParamsJSON(p WakeOnLANParams) (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, err := ParseWakeOnLANParams(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
