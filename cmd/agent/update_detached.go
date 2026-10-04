//go:build windows || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// startInstaller roda o install da versão nova num processo separado deste serviço, com a
// saída em logs/update.log. Ele para este serviço, troca o executável e inicia o novo;
// por isso não pode morrer junto com o serviço (detachedAttr, por SO).
func startInstaller(bin string, args []string, dataDir string) error {
	logPath := filepath.Join(dataDir, "logs", "update.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close() // o filho tem a própria cópia do arquivo aberto
	fmt.Fprintf(logf, "==== %s instalador: %s\n", time.Now().Format(time.RFC3339), bin)

	cmd := exec.Command(bin, append([]string{"install"}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
