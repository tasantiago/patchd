package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
)

// shutdownExe: o caminho absoluto (o Runner recusa nomes soltos, que seriam resolvidos
// pelo PATH de um processo SYSTEM).
func shutdownExe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "shutdown.exe")
}

// newRestart agenda o reinício do job reboot no Windows (Aula 8.7). Na frota do tribunal
// quem instala as atualizações do Windows é o WSUS (RF-30); o patchd cobra o reinício que
// o WSUS deixou pendente. shutdown /r /t: o Windows mostra o aviso a quem estiver logado,
// com o comentário (/c), e o prazo. /d p:2:17 registra no log de eventos (evento 1074) o
// motivo "Sistema operacional: hotfix (planejado)". Um administrador local cancela com
// "shutdown /a"; o agente então informa que o reinício não aconteceu.
// https://learn.microsoft.com/windows-server/administration/windows-commands/shutdown
func newRestart(run platform.Runner) func(ctx context.Context, jobID int64, delay time.Duration) error {
	return func(ctx context.Context, jobID int64, delay time.Duration) error {
		secs := int(delay / time.Second)
		min := int(delay / time.Minute)
		msg := fmt.Sprintf("patchd (job %d): este computador vai reiniciar em %d minuto(s) para concluir atualizações de segurança. Salve seu trabalho.", jobID, min)
		if delay > time.Hour { // hora escolhida pelo usuário (Aula 17.6): o aviso diz a hora
			msg = fmt.Sprintf("patchd (job %d): este computador vai reiniciar às %s para concluir atualizações de segurança, como você escolheu. Salve seu trabalho antes.",
				jobID, time.Now().Add(delay).Format("15:04"))
		}
		_, err := run.Run(ctx, shutdownExe(), "/r", "/t", fmt.Sprint(secs), "/d", "p:2:17", "/c", msg)
		return err
	}
}

// newAbortRestart cancela o reinício agendado (Aula 17.6): um reinício pedido para mais
// cedo toma o lugar do que já estava marcado. shutdown /a só cancela o que está no prazo.
func newAbortRestart(run platform.Runner) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := run.Run(ctx, shutdownExe(), "/a")
		return err
	}
}
