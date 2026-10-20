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
	// A impressão curta confere com a do servidor ("jobs pelo painel ligados") e a do
	// patchd-server job pubkey: chaves diferentes = todo job recusado.
	logger.Info("jobs ligados", "key_id", jobs.KeyID(pub))
	return pub
}

// jobKeyNext lê a chave de jobs seguinte (Aula 20.7), se o build trouxer. Inválida: fica de
// fora, com o motivo no log (a atual continua valendo).
func jobKeyNext(logger *slog.Logger) ed25519.PublicKey {
	if jobs.NextPublicKey == "" {
		return nil
	}
	pub, err := jobs.ParsePublicKey(jobs.NextPublicKey)
	if err != nil {
		logger.Error("chave de jobs seguinte ignorada", "error", err)
		return nil
	}
	logger.Info("chave de jobs seguinte aceita", "key_id", jobs.KeyID(pub))
	return pub
}

// keyIDs: as impressões das chaves aceitas, para o estado do check-in (Aula 20.7). O painel
// conta quantas máquinas já aceitam cada chave antes de o servidor trocar.
func keyIDs(id func(ed25519.PublicKey) string, keys ...ed25519.PublicKey) []string {
	out := []string{}
	for _, k := range keys {
		if k != nil {
			out = append(out, id(k))
		}
	}
	return out
}
