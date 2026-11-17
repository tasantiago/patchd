package winget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Describe: a versão mais nova, o nome e o fabricante do idioma padrão, os tipos x64 que o
// patchd instala (e os que não) e o nome em Programas e Recursos.
func TestDescreverPacote(t *testing.T) {
	const instalador = `PackageIdentifier: VideoLAN.VLC
PackageVersion: 3.0.21
InstallerType: nullsoft
Installers:
- Architecture: x86
  InstallerUrl: https://exemplo.invalid/vlc-x86.exe
  InstallerSha256: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
- Architecture: x64
  InstallerUrl: https://exemplo.invalid/vlc-x64.exe
  InstallerSha256: BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB
  AppsAndFeaturesEntries:
  - DisplayName: VLC media player
    Publisher: VideoLAN
- Architecture: x64
  InstallerType: wix
  InstallerUrl: https://exemplo.invalid/vlc-x64.msi
  InstallerSha256: CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC
- Architecture: x64
  InstallerType: msix
  InstallerUrl: https://exemplo.invalid/vlc.msix
  InstallerSha256: DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD
ManifestType: installer
ManifestVersion: 1.6.0
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/repos/microsoft/winget-pkgs/contents/manifests/v/VideoLAN/VLC":
			_, _ = w.Write([]byte(`[{"name":"3.0.20","type":"dir"},{"name":"3.0.21","type":"dir"},{"name":"Nightly","type":"dir"}]`))
		case "/raw/manifests/v/VideoLAN/VLC/3.0.21/VideoLAN.VLC.installer.yaml":
			_, _ = w.Write([]byte(instalador))
		case "/raw/manifests/v/VideoLAN/VLC/3.0.21/VideoLAN.VLC.yaml":
			_, _ = w.Write([]byte("PackageIdentifier: VideoLAN.VLC\nPackageVersion: 3.0.21\nDefaultLocale: pt-BR\nManifestType: version\n"))
		case "/raw/manifests/v/VideoLAN/VLC/3.0.21/VideoLAN.VLC.locale.pt-BR.yaml":
			_, _ = w.Write([]byte("PackageIdentifier: VideoLAN.VLC\nPackageName: VLC   media player\nPublisher: VideoLAN\nManifestType: defaultLocale\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.APIURL, c.RawURL = srv.URL+"/api", srv.URL+"/raw"
	d, err := c.Describe(context.Background(), "VideoLAN.VLC")
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "3.0.21" || d.Name != "VLC media player" || d.Publisher != "VideoLAN" || strings.Join(d.Types, ",") != "nullsoft,wix" ||
		strings.Join(d.Unsupported, ",") != "msix" || d.DisplayName != "VLC media player" || d.DisplayPublisher != "VideoLAN" {
		t.Errorf("descrição: %+v", d)
	}
	if _, err := c.Describe(context.Background(), "Nao.Existe"); err == nil {
		t.Error("pacote que não existe")
	}
}
