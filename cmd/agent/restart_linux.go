package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
)

const shutdownPath = "/usr/sbin/shutdown"

// newRestart agenda o reinício do job reboot (Aula 8.6) pelo shutdown do systemd: o logind
// guarda o agendamento (ele sobrevive ao agente), avisa os terminais com a mensagem (wall)
// e bloqueia logins novos nos 5 minutos finais. Quem estiver na máquina pode cancelar com
// "shutdown -c"; o agente então informa que o reinício não aconteceu. O texto é do agente:
// o servidor só manda o prazo.
func newRestart(run platform.Runner) func(ctx context.Context, jobID int64, delay time.Duration) error {
	return func(ctx context.Context, jobID int64, delay time.Duration) error {
		min := int(delay / time.Minute)
		msg := fmt.Sprintf("patchd (job %d): esta máquina vai reiniciar em %d minuto(s) para concluir atualizações de segurança. Salve seu trabalho.", jobID, min)
		_, err := run.Run(ctx, shutdownPath, "-r", fmt.Sprintf("+%d", min), msg)
		return err
	}
}
