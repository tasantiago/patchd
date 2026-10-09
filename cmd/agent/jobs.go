package main

import (
	"crypto/ed25519"
	"log/slog"

	"github.com/tasantiago/patchd/internal/jobs"
)

// jobKey lê a chave pública de jobs embutida no build (Aula 8.1). Sem ela, o agente
// continua medindo e recusa todo job, informando o motivo ao servidor.
func jobKey(logger *slog.Logger) ed25519.PublicKey {
	if jobs.PublicKey == "" {
		logger.Warn("jobs desligados: este executável foi compilado sem chave de jobs (PATCHD_JOB_PUBKEY); todo job será recusado")
		return nil
	}
	pub, err := jobs.ParsePublicKey(jobs.PublicKey)
	if err != nil {
		logger.Error("jobs desligados", "error", err)
		return nil
	}
	return pub
}
