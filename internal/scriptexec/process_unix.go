//go:build linux || darwin

package scriptexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/tasantiago/patchd/internal/jobs"
)

func runProcess(_ context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	// Também recolhe filhos deixados pelo script ao sair. Pacotes devem aguardar
	// seus instaladores, em vez de deixá-los usando a pasta que será removida.
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	return cmd.Wait()
}
