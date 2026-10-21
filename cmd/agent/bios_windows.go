package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/bioswrite"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
)

// biosRunner: o executor do job bios_password (Aula 21.5), com a caixa desta máquina. Sem a
// caixa, nil: o agente recusa esses jobs, e o servidor não os cria (o painel não vê a chave).
func biosRunner(logger *slog.Logger, boxPriv []byte) func(context.Context, int64, jobs.BIOSPasswordParams) (string, error) {
	if boxPriv == nil {
		return nil
	}
	pub, err := machinebox.Public(boxPriv)
	if err != nil {
		return nil
	}
	r := bioswrite.Runner{
		BoxKeyID: machinebox.KeyID(pub),
		Open:     func(purpose string, sealed []byte) ([]byte, error) { return machinebox.Open(boxPriv, purpose, sealed) },
		Detect:   bioswrite.Detect,
		Logger:   logger,
	}
	return r.Run
}
