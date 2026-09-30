package inventory

import (
	"context"
	"io/fs"
	"testing"
)

// Info.plist do Safari reduzido às chaves usadas; a versão é a real do Mac do laboratório.
const safariInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.apple.Safari</string>
	<key>CFBundleName</key>
	<string>Safari</string>
	<key>CFBundleShortVersionString</key>
	<string>26.5.2</string>
</dict>
</plist>`

const comandoSafariPlist = "/usr/bin/plutil -convert xml1 -o - /System/Cryptexes/App/System/Applications/Safari.app/Contents/Info.plist"

func TestCollectCryptexApps(t *testing.T) {
	run := runnerFalso{comandoSafariPlist: {saida: safariInfoPlist}}
	listDir := func(string) ([]string, error) { return []string{"Safari.app", "LEIA-ME.txt"}, nil }

	list, err := collectCryptexApps(context.Background(), run, listDir)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("esperado só o Safari (o .txt fica fora): %+v", list)
	}
	s := list[0]
	if s.Name != "Safari" || s.Version != "26.5.2" || s.Publisher != "Apple" || s.SystemComponent {
		t.Errorf("Safari errado (precisa ficar visível, com versão): %+v", s)
	}
	if s.Source != "/System/Cryptexes/App/System/Applications/Safari.app" {
		t.Errorf("fonte deveria ser o caminho real no cryptex: %q", s.Source)
	}
}

func TestCollectCryptexAppsSemCryptex(t *testing.T) {
	listDir := func(string) ([]string, error) { return nil, fs.ErrNotExist }
	list, err := collectCryptexApps(context.Background(), runnerFalso{}, listDir)
	if err != nil || list != nil {
		t.Errorf("macOS sem cryptex não é erro: %+v, %v", list, err)
	}
}
