package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/release"
)

// newUpdater monta a atualização automática. Sem a chave pública embutida no build, ou com
// uma chave inválida, ela fica desligada, e o motivo vai para o log uma vez, na partida.
func newUpdater(logger *slog.Logger, client *agent.Client, opts loopOptions, current string) func(context.Context, string) {
	if release.PublicKey == "" {
		logger.Warn("atualização automática desligada: este executável foi compilado sem chave pública (PATCHD_UPDATE_PUBKEY)")
		return nil
	}
	pub, err := release.ParsePublicKey(release.PublicKey)
	if err != nil {
		logger.Error("atualização automática desligada", "error", err)
		return nil
	}
	u := &agent.Updater{
		Client:    client,
		DataDir:   opts.DataDir,
		Current:   current,
		PublicKey: pub,
		Install:   func(bin string) error { return startInstaller(bin, opts.ServiceArgs, opts.DataDir) },
		Logger:    logger,
	}
	u.CleanUpdates()
	return u.Offer
}
