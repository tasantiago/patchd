package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/wol"
)

// WakeOnLANRunner devolve o executor do job wake_on_lan (docs/wake-on-lan.md, Aula 22.2):
// confere os MACs e a rede de novo (o Verify já conferiu; aqui é o mesmo código, por
// garantia) e manda o pacote pelo broadcast das redes desta máquina. A rede pedida que não é
// desta máquina vira recusa: nada sai pela rede.
func WakeOnLANRunner(s *wol.Sender) func(ctx context.Context, p jobs.WakeOnLANParams) (string, error) {
	return func(ctx context.Context, p jobs.WakeOnLANParams) (string, error) {
		network, err := p.Prefix()
		if err != nil {
			return "", fmt.Errorf("%w: %v", jobs.ErrRefused, err)
		}
		macs := make([]net.HardwareAddr, 0, len(p.MACs))
		for _, m := range p.MACs {
			hw, err := wol.ParseMAC(m)
			if err != nil {
				return "", fmt.Errorf("%w: %v", jobs.ErrRefused, err)
			}
			macs = append(macs, hw)
		}
		res, err := s.Wake(ctx, macs, network)
		switch {
		case errors.Is(err, wol.ErrNoNetwork):
			return "", fmt.Errorf("%w: %v", jobs.ErrRefused, err)
		case err != nil:
			return "", err
		}
		return fmt.Sprintf("pacote mágico para %s (%s) por %s; %d datagrama(s) na porta %d",
			p.Target, strings.Join(p.MACs, ", "), strings.Join(res.Networks, "; "), res.Sent, wol.Port), nil
	}
}
