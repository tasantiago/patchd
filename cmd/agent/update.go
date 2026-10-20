package main

import (
	"context"
	"crypto/ed25519"
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
		Client:        client,
		DataDir:       opts.DataDir,
		Current:       current,
		PublicKey:     pub,
		PublicKeyNext: releaseKeyNext(logger),
		Install:       func(bin string) error { return startInstaller(bin, opts.ServiceArgs, opts.DataDir) },
		Logger:        logger,
	}
	u.CleanUpdates()
	return u.Offer
}

// releaseKeyNext lê a chave de release seguinte (Aula 20.7), se o build trouxer.
func releaseKeyNext(logger *slog.Logger) ed25519.PublicKey {
	if release.NextPublicKey == "" {
		return nil
	}
	pub, err := release.ParsePublicKey(release.NextPublicKey)
	if err != nil {
		logger.Error("chave de release seguinte ignorada", "error", err)
		return nil
	}
	return pub
}

// releaseKeys: as chaves de release embutidas (a atual e a seguinte), para o check-in.
func releaseKeys(logger *slog.Logger) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	if pub, err := release.ParsePublicKey(release.PublicKey); err == nil {
		keys = append(keys, pub)
	}
	return append(keys, releaseKeyNext(logger))
}
