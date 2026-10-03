package patch

import (
	"context"

	"github.com/tasantiago/patchd/internal/protocol"
)

const darwinSysctlPath = "/usr/sbin/sysctl"

// macNoRebootSignal explica por que o macOS não tem decisão de reinício pendente.
const macNoRebootSignal = "o macOS não expõe reinício pendente de forma documentada: as atualizações " +
	"do sistema são aplicadas durante o próprio reinício; o que vai exigir reinício aparece " +
	"como restart_required nos itens da busca"

// Reboot: sem sinal confiável, a decisão fica nula; vai a última inicialização.
func (s darwinScanner) Reboot(ctx context.Context) (protocol.RebootStatus, error) {
	st := protocol.RebootStatus{Signals: []protocol.RebootSignal{}, Note: macNoRebootSignal}
	if out, err := s.run.Run(ctx, darwinSysctlPath, "-n", "kern.boottime"); err == nil {
		if t, err := parseKernBoottime(out); err == nil {
			st.LastBoot = &t
		}
	}
	return st, nil
}
