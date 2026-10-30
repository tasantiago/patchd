package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
)

const offlineFile = "offline-enrollment.json"

type offlineEnrollment struct {
	ServerURL string
	Token     string
	CAFile    string
	HoldImage bool
}

func readOffline(dir string) (offlineEnrollment, error) {
	var p offlineEnrollment
	b, err := os.ReadFile(filepath.Join(dir, offlineFile))
	if err != nil {
		return p, err
	}
	if len(b) > 16384 || json.Unmarshal(b, &p) != nil || p.ServerURL == "" || p.Token == "" {
		return p, errors.New("configuração offline ilegível")
	}
	return p, nil
}
func saveOffline(dir string, p offlineEnrollment) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return credential.SaveSecret(dir, offlineFile, b)
}
func prepareOfflineInstall(cfg agentConfig, args []string, look config.Lookup) ([]string, error) {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("instalação offline exige -server com URL HTTPS")
	}
	for _, name := range []string{credential.FileName, credential.BoxKeyFile} {
		if _, err := os.Lstat(filepath.Join(cfg.DataDir, name)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("a pasta já tem identidade ou chave privada; use uma imagem limpa, sem apagar a identidade de uma estação em uso")
		}
	}
	token, err := enrollToken(cfg.EnrollTokenFile, look)
	if err != nil {
		return nil, errors.New("informe um arquivo válido de token para a instalação offline")
	}
	p := offlineEnrollment{ServerURL: cfg.ServerURL, Token: token, HoldImage: cfg.ImageInstall}
	var ca []byte
	if cfg.CAFile != "" {
		var err error
		ca, err = os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, errors.New("não foi possível ler a CA")
		}
		p.CAFile = filepath.Join(cfg.DataDir, "enrollment-ca.crt")
	}
	if old, err := readOffline(cfg.DataDir); err == nil {
		// Re-running installation must never rearm an image that was already activated.
		if cfg.ImageInstall && !old.HoldImage {
			return nil, errors.New("imagem já ativada; prepare uma imagem limpa antes da captura")
		}
		if p.CAFile != "" {
			stored, err := os.ReadFile(p.CAFile)
			if err != nil || !bytes.Equal(stored, ca) {
				return nil, errors.New("a CA da configuração offline existente é diferente")
			}
		}
		if old.ServerURL != p.ServerURL || old.Token != p.Token || old.CAFile != p.CAFile {
			return nil, errors.New("já existe configuração offline diferente nesta pasta")
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if p.CAFile != "" {
			if err := credential.SaveSecret(cfg.DataDir, "enrollment-ca.crt", ca); err != nil {
				return nil, err
			}
		}
		if err := saveOffline(cfg.DataDir, p); err != nil {
			return nil, err
		}
	}
	filtered := serviceArgs(args)
	out := []string{}
	for i := 0; i < len(filtered); i++ {
		a := filtered[i]
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && (name == "ca-file" || name == "data-dir") {
			if !hasValue {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	out = append(out, "-data-dir", cfg.DataDir)
	if p.CAFile != "" {
		out = append(out, "-ca-file", p.CAFile)
	}
	return out, nil
}

func activateOffline(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	cfg, err := loadAgentConfig(args, look, stderr)
	if err != nil {
		return exitConfig
	}
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	unlock, err := lockDataDir(cfg.DataDir, installLockWait, stderr)
	if err != nil {
		return exitRuntime
	}
	defer unlock()
	p, err := readOffline(cfg.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := credential.Load(cfg.DataDir); err == nil {
			fmt.Fprintln(stdout, "agente já registrado")
			return exitOK
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "configuração offline não encontrada ou ilegível")
		return exitConfig
	}
	p.HoldImage = false
	if err := saveOffline(cfg.DataDir, p); err != nil {
		return exitRuntime
	}
	fmt.Fprintln(stdout, "registro offline liberado; o serviço registrará esta estação quando alcançar o servidor")
	return exitOK
}

func offlineAttempt(ctx context.Context, dir, version string) (credential.Credential, error) {
	unlock, err := lockDataDir(dir, 0, io.Discard)
	if err != nil {
		return credential.Credential{}, err
	}
	defer unlock()
	if c, err := credential.Load(dir); err == nil {
		return c, nil
	} else if !errors.Is(err, credential.ErrNotEnrolled) {
		return c, err
	}
	p, err := readOffline(dir)
	if err != nil {
		return credential.Credential{}, err
	}
	if p.HoldImage {
		return credential.Credential{}, errors.New("imagem aguardando activate")
	}
	if err := setEnrollCA(p.CAFile); err != nil {
		return credential.Credential{}, err
	}
	c, err := enrollMachineContext(ctx, p.ServerURL, dir, p.Token, version, io.Discard)
	if err == nil {
		_ = os.Remove(filepath.Join(dir, offlineFile))
	}
	return c, err
}

func waitOfflineEnrollment(ctx context.Context, logger *slog.Logger, dir, version string) (credential.Credential, error) {
	delay := 2 * time.Second
	for {
		p, err := readOffline(dir)
		if errors.Is(err, os.ErrNotExist) {
			return credential.Credential{}, credential.ErrNotEnrolled
		}
		if err != nil {
			return credential.Credential{}, errors.New("configuração offline ilegível")
		}
		if p.HoldImage {
			logger.Info("imagem preparada: aguardando activate após Sysprep")
		} else {
			c, err := offlineAttempt(ctx, dir, version)
			if err == nil {
				logger.Info("registro adiado concluído")
				return c, nil
			}
			logger.Warn("aguardando registro: confira conectividade HTTPS, CA e validade do token", "nova_tentativa_em", delay.String())
		}
		pause := delay
		if p.HoldImage {
			pause = 30 * time.Second
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return credential.Credential{}, ctx.Err()
		case <-timer.C:
		}
		if !p.HoldImage && delay < 5*time.Minute {
			delay *= 2
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		}
	}
}
