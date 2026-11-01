//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func userAppRenamer() func(string) (bool, error) { return nil }
func managedAppPath() string                     { return "" }

func syncUserApp(logger *slog.Logger, enabled bool, name string) {
	if os.Geteuid() != 0 {
		return
	}
	path := userAutostartPath()
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Warn("padronização: remover início de sessão", "error", err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		logger.Warn("padronização: executável indisponível", "error", err)
		return
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return
	}
	data, err := unixAutostart(exe, runtime.GOOS)
	if err != nil {
		logger.Warn("padronização: configurar início de sessão", "error", err)
		return
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return
	}
	if err := writeRootFile(path, data, 0644); err != nil {
		logger.Warn("padronização: gravar início de sessão", "error", err)
	}
}

func unixAutostart(exe, platform string) ([]byte, error) {
	if !filepath.IsAbs(exe) || strings.ContainsAny(exe, "\r\n\x00") {
		return nil, fmt.Errorf("caminho do agente inválido")
	}
	if platform == "darwin" {
		var b bytes.Buffer
		if err := xml.EscapeText(&b, []byte(exe)); err != nil {
			return nil, err
		}
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>com.patchd.standardization</string><key>ProgramArguments</key><array><string>` + b.String() + `</string><string>app</string><string>-no-tray</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>`), nil
	}
	quoted := `"` + strings.NewReplacer("\\", "\\\\\\\\", "\"", "\\\\\"", "`", "\\\\`", "$", "\\\\$", "%", "%%").Replace(exe) + `"`
	return []byte("[Desktop Entry]\nType=Application\nName=Padronização patchd\nExec=" + quoted + " app -no-tray\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n"), nil
}

func userAutostartPath() string {
	if runtime.GOOS == "darwin" {
		return "/Library/LaunchAgents/com.patchd.standardization.plist"
	}
	return "/etc/xdg/autostart/patchd-standardization.desktop"
}
