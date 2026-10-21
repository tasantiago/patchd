package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/machinebox"
)

// A caixa da máquina (Aula 21.3): a chave privada fica na pasta de dados, protegida como a
// credencial (só SYSTEM e Administradores, ou root); a pública vai no estado de cada
// check-in. O servidor fecha segredos para esta máquina com ela (a senha da BIOS, 21.4).

// loadBoxKey lê a chave privada; sem arquivo, cria. Arquivo ilegível: guardado à parte
// (.ruim-<hora>) e trocado por uma chave nova, com aviso; o que tiver sido fechado para a
// antiga deixa de abrir, e o servidor fecha de novo para a nova.
func loadBoxKey(logger *slog.Logger, dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, machinebox.FileName)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		priv, perr := machinebox.ParsePrivate(string(b))
		if perr == nil {
			return priv, nil
		}
		bad := fmt.Sprintf("%s.ruim-%d", path, time.Now().Unix())
		if err := os.Rename(path, bad); err != nil {
			return nil, fmt.Errorf("chave da caixa ilegível (%v) e não deu para tirá-la do caminho: %w", perr, err)
		}
		logger.Warn("chave da caixa ilegível: guardada à parte e trocada por uma nova", "error", perr, "arquivo", filepath.Base(bad))
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	priv, err := machinebox.Generate()
	if err != nil {
		return nil, err
	}
	if err := credential.SaveSecret(dataDir, credential.BoxKeyFile, []byte(machinebox.EncodePrivate(priv))); err != nil {
		return nil, err
	}
	pub, _ := machinebox.Public(priv)
	logger.Info("chave da caixa criada", "key_id", machinebox.KeyID(pub))
	return priv, nil
}

// boxPublic: a chave pública para o estado ("" se não deu para ter a chave: o resto do agente
// segue, e o painel mostra a máquina sem a caixa).
func boxPublic(logger *slog.Logger, dataDir string) string {
	priv, err := loadBoxKey(logger, dataDir)
	if err != nil {
		logger.Error("chave da caixa indisponível: segredos não poderão ser entregues a esta máquina", "error", err)
		return ""
	}
	pub, err := machinebox.Public(priv)
	if err != nil {
		return ""
	}
	return machinebox.EncodePublic(pub)
}
