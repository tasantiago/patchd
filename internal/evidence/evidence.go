// Package evidence coleta, no agente, os fatos de identidade que a máquina apresenta no
// enrollment (Aula 4.3). Nenhum deles é a identidade: o servidor emite o ID e usa estes
// fatos para reconhecer a mesma máquina e denunciar clones.
//
// Os valores vão crus: a normalização (genéricos, ordem de bytes do UUID) é do servidor,
// que pode reavaliar quando a regra mudar.
package evidence

import (
	"context"
	"net"
	"os"
	"sort"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Collect devolve as evidências do SO atual e as falhas parciais (uma por fonte que
// não pôde ser lida). Falha parcial não impede o registro: o servidor decide se o que
// chegou é suficiente.
func Collect(ctx context.Context, run platform.Runner) (protocol.IdentityEvidence, []string) {
	e, problems := collectOS(ctx, run)
	if h, err := os.Hostname(); err == nil {
		e.Hostname = h
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		problems = append(problems, "interfaces de rede: "+err.Error())
	}
	e.MACs = factoryMACs(ifaces)
	return e, problems
}

// factoryMACs devolve os MACs de fábrica das interfaces, ordenados e sem repetição.
// Ficam de fora a interface de loopback e os endereços que identity.NormalizeMAC descarta
// (virtuais, aleatórios, multicast). MAC é evidência fraca: um adaptador USB ou uma dock
// passa de notebook em notebook, e por isso o servidor nunca o usa para relacionar máquinas.
func factoryMACs(ifaces []net.Interface) []string {
	seen := map[string]bool{}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if m, ok := identity.NormalizeMAC(ifc.HardwareAddr.String()); ok && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
