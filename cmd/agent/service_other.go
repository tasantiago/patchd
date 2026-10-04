//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/tasantiago/patchd/internal/config"
)

// No Linux e no macOS, o serviço (systemd, launchd) chega na Aula 5.2. Por enquanto,
// "service run" funciona em primeiro plano, e install/uninstall avisam.

func serviceInstall(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "patchd-agent: a instalação como serviço neste sistema chega na Aula 5.2 (systemd e launchd)")
	return exitConfig
}

func serviceUninstall(stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "patchd-agent: a instalação como serviço neste sistema chega na Aula 5.2 (systemd e launchd)")
	return exitConfig
}

func isServiceProcess() bool { return false }

func runAsService(logger *slog.Logger, loop func(context.Context) int) int {
	return loop(context.Background())
}

// protectDataDir: só o dono (root, no serviço) entra na pasta de dados.
func protectDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
