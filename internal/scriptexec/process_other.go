//go:build !windows && !linux && !darwin

package scriptexec

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/tasantiago/patchd/internal/jobs"
)

func runProcess(context.Context, *exec.Cmd) error {
	return fmt.Errorf("%w: execução de scripts disponível em Windows, Linux e macOS", jobs.ErrNotStarted)
}
