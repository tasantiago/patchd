package tpfetch

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// Aula 23.5: com Types, vale o primeiro tipo da lista que o manifesto tiver; sem, o da regra.
func TestPick(t *testing.T) {
	m := winget.Manifest{PackageIdentifier: "7zip.7zip", PackageVersion: "26.05", Installers: []winget.Installer{
		{Index: 0, Architecture: "x64", Type: "exe", URL: "https://www.7-zip.org/a/7z2605-x64.exe", SHA256: strings.Repeat("a", 64),
			Switches: map[string]string{"Silent": "/S"}},
		{Index: 1, Architecture: "x64", Type: "wix", URL: "https://www.7-zip.org/a/7z2605-x64.msi", SHA256: strings.Repeat("b", 64)},
	}}
	regra := thirdparty.App{ID: "7zip", Install: &thirdparty.Install{Winget: "7zip.7zip", Type: "exe"}}
	if in, err := Pick(m, regra, Options{Arch: "x64"}); err != nil || in.Type != "exe" {
		t.Errorf("o da regra: %+v %v", in, err)
	}
	if in, err := Pick(m, regra, Options{Arch: "x64", Types: []string{"msi", "wix"}}); err != nil || in.Type != "wix" {
		t.Errorf("a família MSI: %+v %v", in, err)
	}
	if _, err := Pick(m, regra, Options{Arch: "x64", Types: []string{"nullsoft", "inno"}}); err == nil ||
		!strings.Contains(err.Error(), "nenhum instalador nullsoft ou inno serve") {
		t.Errorf("tipo que o manifesto não tem: %v", err)
	}
}

func TestURLHasVersion(t *testing.T) {
	if !URLHasVersion("https://www.7-zip.org/a/7z2605-x64.exe", "26.05") || URLHasVersion("https://x/latest.msi", "26.05") {
		t.Error("URLHasVersion")
	}
}
