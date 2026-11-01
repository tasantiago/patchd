//go:build linux || darwin

package shortcuts

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

func UserManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if os.Geteuid() == 0 {
		return nil, errors.New("atalhos precisam da sessão do usuário, sem sudo")
	}
	desktop := filepath.Join(home, "Desktop")
	if runtime.GOOS == "linux" {
		config, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		if b, err := os.ReadFile(filepath.Join(config, "user-dirs.dirs")); err == nil {
			desktop = xdgDesktop(home, string(b))
		}
	}
	state, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Manager{Desktop: desktop, StatePath: filepath.Join(state, "patchd", "standardization", "shortcuts.json"), Write: writeUnix}, nil
}

// user-dirs.dirs é lido como dados; nenhuma expansão de shell é executada.
func xdgDesktop(home, config string) string {
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "XDG_DESKTOP_DIR=") {
			continue
		}
		p := strings.TrimPrefix(line, "XDG_DESKTOP_DIR=")
		if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
			break
		}
		p = p[1 : len(p)-1]
		p = strings.ReplaceAll(p, "$HOME", home)
		if strings.ContainsAny(p, "$`\\") || !filepath.IsAbs(p) || filepath.Clean(p) == filepath.Clean(home) {
			break
		}
		return filepath.Clean(p)
	}
	return filepath.Join(home, "Desktop")
}

func unixTarget(target string) (string, error) {
	if strings.HasPrefix(target, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		target = filepath.Join(home, strings.TrimPrefix(target, "~/"))
	}
	if !filepath.IsAbs(target) {
		return "", errors.New("caminho Unix precisa ser absoluto")
	}
	return target, nil
}

func desktopEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(s)
}
func execQuote(s string) string {
	return `"` + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(s) + `"`
}

func writeUnix(path string, s protocol.StandardShortcut) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if !s.SupportsOS(runtime.GOOS) {
		return errors.New("atalho incompatível com este sistema")
	}
	target := s.Target
	if s.Kind != "site" {
		var err error
		target, err = unixTarget(target)
		if err != nil {
			return err
		}
	}
	if runtime.GOOS == "darwin" {
		if s.Kind != "site" {
			target = (&url.URL{Scheme: "file", Path: target}).String()
		}
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(target))
		data := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>URL</key><string>` + b.String() + `</string></dict></plist>`
		return os.WriteFile(path, []byte(data), 0600)
	}
	icon := s.Icon
	if icon == "" {
		icon = s.Kind
	}
	icons := map[string]string{"site": "web-browser", "aplicativo": "application-x-executable", "pasta": "folder", "documento": "text-x-generic"}
	data := fmt.Sprintf("[Desktop Entry]\nVersion=1.0\nName=%s\nIcon=%s\n", desktopEscape(s.Name), icons[icon])
	if s.Kind == "aplicativo" {
		data += "Type=Application\nExec=" + desktopEscape(execQuote(target)) + "\nTerminal=false\n"
	} else {
		if s.Kind == "pasta" {
			target = (&url.URL{Scheme: "file", Path: target}).String()
		}
		data += "Type=Link\nURL=" + desktopEscape(target) + "\n"
	}
	// #nosec G306 -- um launcher .desktop precisa do bit executável; é escrito no perfil do próprio usuário.
	return os.WriteFile(path, []byte(data), 0700)
}
