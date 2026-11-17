package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shortcuts"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// O app do usuário (Aula 17.4) é o patchd-app.exe, ao lado do patchd-agent.exe: um executável
// próprio (cmd/app), que o agente traz embutido e grava sem mudar nada (appcopy.go). Em
// arquivo à parte também porque o Windows não deixa trocar um executável em uso: com o app
// aberto nas sessões, a atualização do agente falharia. A cópia em uso pode ser renomeada (o
// Windows deixa); a nova entra no lugar e a antiga sai quando ninguém mais a usa. Quem abre o
// app no logon é a chave Run do HKLM: vale para todo usuário que entra.

const (
	runKey   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	runValue = "patchd-central-de-software"
)

func userAppPath() string { return filepath.Join(filepath.Dir(installedBinary()), "patchd-app.exe") }

// managedAppPath: o caminho da cópia que o serviço mantém, se é dela que o app está rodando
// (Aula 18.8); vazio quando o app roda de outro lugar (um executável de teste): nada a vigiar.
func managedAppPath() string {
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Clean(exe), filepath.Clean(userAppPath())) {
		return ""
	}
	return userAppPath()
}

// syncUserApp põe (ou tira) o app do logon. Só o serviço instalado mexe nisso.
func syncUserApp(logger *slog.Logger, enabled bool, name string) {
	if !runningInstalledCopy() {
		return
	}
	if !enabled {
		if err := removeCentralLink(); err != nil {
			logger.Warn("atalho da Central na área de trabalho não apagado", "error", err)
		}
		if err := removeUserApp(); err != nil {
			logger.Warn("app do usuário: remoção incompleta", "error", err)
		}
		logger.Debug("app do usuário desligado (-user-app=false)")
		return
	}
	changed, err := refreshUserApp()
	if err != nil {
		logger.Error("app do usuário: cópia não atualizada", "error", err)
		return
	}
	cmd := `"` + userAppPath() + `" app`
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		logger.Error("app do usuário: chave Run", "error", err)
		return
	}
	defer k.Close()
	if cur, _, err := k.GetStringValue(runValue); err != nil || cur != cmd {
		if err := k.SetStringValue(runValue, cmd); err != nil {
			logger.Error("app do usuário: chave Run", "error", err)
			return
		}
		changed = true
	}
	if changed {
		logger.Info("app do usuário instalado: abre no próximo logon de cada usuário", "exe", userAppPath())
	}
	syncCentralLink(logger, name)
}

// syncCentralLink põe o atalho da Central na área de trabalho de todos os usuários, com o
// nome da Central no painel (o que não vale como nome de arquivo fica com o padrão). O atalho
// abre o patchd-app.exe sem argumentos: a página da Central.
func syncCentralLink(logger *slog.Logger, name string) {
	if !shortcuts.ValidLinkName(name) {
		name = protocol.DefaultAppName
	}
	link, err := shortcuts.CentralLink(userAppPath())
	if err != nil {
		logger.Warn("atalho da Central na área de trabalho", "error", err)
		return
	}
	state, removed, err := link.Sync(name)
	for _, r := range removed {
		logger.Info("atalho da Central com o nome antigo apagado", "nome", r)
	}
	switch {
	case err != nil:
		logger.Warn("atalho da Central na área de trabalho não criado", "nome", name, "error", err)
	case state == shortcuts.AppLinkCreated:
		logger.Info("atalho da Central criado na área de trabalho de todos os usuários", "nome", name)
	}
}

// userAppRenamer: desde que a Central é um executável próprio, o nome do painel não muda o
// executável (o nome que o Windows mostra é o do build; a página usa o do painel). Muda o
// atalho na área de trabalho, que leva o nome do painel.
func userAppRenamer(logger *slog.Logger) func(string) (bool, error) {
	return func(name string) (bool, error) {
		syncCentralLink(logger, name)
		return false, nil
	}
}

// refreshUserApp deixa o patchd-app.exe igual à Central embutida no agente, byte a byte.
func refreshUserApp() (bool, error) {
	img, err := userAppImage(embeddedAppGz, embeddedAppSum)
	if err != nil {
		return false, err
	}
	return writeUserApp(userAppPath(), img)
}

// removeUserApp tira o app do logon e apaga a cópia. A cópia em uso numa sessão é renomeada
// e marcada para sair no próximo reinício: o nome patchd-app.exe fica livre (a reinstalação
// da atualização do agente passa por aqui e põe a cópia nova logo em seguida).
func removeUserApp() error {
	var errs []error
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE)
	if err == nil {
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		_ = k.Close()
	}
	dest := userAppPath()
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if err := os.Rename(dest, fmt.Sprintf("%s.old-%d", dest, time.Now().UnixNano())); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(dest), err))
		}
	}
	cleanOldApps(dest)
	old, _ := filepath.Glob(dest + ".old-*")
	for _, f := range old { // as que ainda estão em uso
		p, _ := windows.UTF16PtrFromString(f)
		if err := windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(f), err))
		}
	}
	return errors.Join(errs...)
}

// removeCentralLink apaga o atalho da Central (a Central desligada, o agente desinstalado). A
// reinstalação da atualização do agente não passa por aqui: o atalho fica onde o usuário o pôs.
func removeCentralLink() error {
	link, err := shortcuts.CentralLink(userAppPath())
	if err != nil {
		return err
	}
	_, err = link.Remove()
	return err
}
