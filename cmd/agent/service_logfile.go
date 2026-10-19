//go:build windows || darwin

package main

import (
	"io"
	"path/filepath"

	"github.com/tasantiago/patchd/internal/logging"
)

// rotatingLog abre o log rotativo na pasta de dados (Windows e macOS, que não têm um
// journal que o serviço use por padrão).
func rotatingLog(dataDir string) (io.Writer, func(), error) {
	rf, err := logging.OpenRotating(filepath.Join(dataDir, "logs", "agent.log"), logFileMaxSize, logFileKeep)
	if err != nil {
		return nil, nil, err
	}
	return rf, func() { _ = rf.Close() }, nil
}
