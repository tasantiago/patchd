package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/store"
)

// systemInfo: o que a aba Sistema da Configuração mostra e só o processo sabe (Aula 15.2).
func systemInfo(info buildinfo.Info, cfg serverConfig, st any, certs *certReloader) api.SystemInfo {
	si := api.SystemInfo{Version: info.Version, Commit: info.Commit, GoVersion: info.GoVersion, StartedAt: time.Now().UTC(),
		Retention: describeRetention(cfg.Retention)}
	_, si.Database = st.(*store.Postgres)
	switch {
	case !si.Database:
		si.Catalog = "sem banco: sem catálogo"
	case cfg.CatalogInterval == 0:
		si.Catalog = "sincronização automática desligada (patchd-server catalog sync)"
	default:
		si.Catalog = "sincronizado a cada " + cfg.CatalogInterval.String()
	}
	if pg, ok := st.(*store.Postgres); ok {
		si.Schema = store.SchemaVersion()
		si.Backups = backupStatus(pg) // Aula 20.8, F4
	}
	if certs != nil {
		si.TLSNotAfter = certs.NotAfter
	}
	return si
}

// loadAgentCA lê a CA oferecida às máquinas (PATCHD_AGENT_CA_FILE): só certificados em PEM,
// sem chave privada (um arquivo trocado por engano não vira download no painel).
func loadAgentCA(path string) ([]byte, int, error) {
	if !filepath.IsAbs(path) {
		return nil, 0, fmt.Errorf("o caminho precisa ser absoluto (recebido %q)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) > 1<<20 {
		return nil, 0, errors.New("arquivo grande demais para uma CA (mais de 1 MiB)")
	}
	n, rest := 0, data
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, 0, fmt.Errorf("bloco %q no arquivo: só certificados (nunca a chave privada)", b.Type)
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return nil, 0, fmt.Errorf("certificado %d ilegível: %w", n+1, err)
		}
		n++
	}
	if n == 0 {
		return nil, 0, errors.New("nenhum certificado em PEM no arquivo")
	}
	return data, n, nil
}

// backupStatus: a cópia e o ensaio de restauração mais recentes, para Configuração › Sistema.
func backupStatus(pg *store.Postgres) func(context.Context) (api.BackupStatus, error) {
	return func(ctx context.Context) (api.BackupStatus, error) {
		var b api.BackupStatus
		var err error
		if b.Last, b.HasLast, err = pg.LastBackup(ctx); err != nil {
			return b, err
		}
		b.Test, b.HasTest, err = pg.LastRestoreTest(ctx)
		return b, err
	}
}
