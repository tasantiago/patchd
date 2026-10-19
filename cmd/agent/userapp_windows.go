package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// O app do usuário (Aula 17.4) roda de uma cópia do agente, patchd-app.exe, ao lado do
// patchd-agent.exe. Uma cópia à parte porque o Windows não deixa trocar um executável em
// uso: com o app aberto nas sessões, a atualização do agente falharia. A cópia em uso pode
// ser renomeada (o Windows deixa); a nova entra no lugar e a antiga sai quando ninguém mais a
// usa. Quem abre o app no logon é a chave Run do HKLM: vale para todo usuário que entra.

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
func syncUserApp(logger *slog.Logger, enabled bool) {
	if !runningInstalledCopy() {
		return
	}
	if !enabled {
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
}

// refreshUserApp deixa o patchd-app.exe igual ao executável do serviço, mas marcado como
// programa gráfico (guiImage): aberto no logon, ele não mostra a janela preta do console.
func refreshUserApp() (bool, error) {
	dest := userAppPath()
	cleanOldApps(dest)
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	img, err := os.ReadFile(self)
	if err != nil {
		return false, err
	}
	if img, err = guiImage(img); err != nil {
		return false, err
	}
	if cur, err := os.ReadFile(dest); err == nil && bytes.Equal(cur, img) {
		return false, nil
	}
	if _, err := os.Stat(dest); err == nil {
		// Em uso numa sessão: renomear funciona; apagar, não.
		if err := os.Rename(dest, fmt.Sprintf("%s.old-%d", dest, time.Now().UnixNano())); err != nil {
			return false, fmt.Errorf("tirar a cópia antiga do caminho: %w", err)
		}
	}
	tmp := dest + ".novo"
	// #nosec G306 G703 -- a cópia da Central para o usuário: executável; dest é fixo (Program Files)
	if err := os.WriteFile(tmp, img, 0o755); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, os.Rename(tmp, dest)
}

// cleanOldApps apaga as cópias antigas que ninguém mais usa; as em uso ficam para a próxima.
func cleanOldApps(dest string) {
	old, _ := filepath.Glob(dest + ".old-*")
	for _, f := range old {
		_ = os.Remove(f)
	}
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
