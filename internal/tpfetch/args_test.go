package tpfetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// install.arguments: o instalador guardado leva os argumentos da regra, não os do winget (o
// Temurin sem o ADDLOCAL=ALL); sem eles, os do winget, com o Custom.
func TestArgumentosDaRegra(t *testing.T) {
	msi := []byte("conteúdo de teste do msi")
	sum := sha256.Sum256(msi)
	hexSum := hex.EncodeToString(sum[:])
	var yaml string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/e/EclipseAdoptium/Temurin/11/JDK/11.0.33.10/EclipseAdoptium.Temurin.11.JDK.installer.yaml":
			_, _ = w.Write([]byte(yaml))
		case "/OpenJDK11U-jdk_x64_windows_hotspot_11.0.33_10.msi":
			_, _ = w.Write(msi)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	yaml = fmt.Sprintf(`PackageIdentifier: EclipseAdoptium.Temurin.11.JDK
PackageVersion: 11.0.33.10
InstallerType: wix
Scope: machine
InstallerSwitches:
  Custom: ADDLOCAL=ALL
Installers:
- Architecture: x64
  InstallerUrl: %s/OpenJDK11U-jdk_x64_windows_hotspot_11.0.33_10.msi
  InstallerSha256: %s
ManifestType: installer
ManifestVersion: 1.6.0
`, srv.URL, strings.ToUpper(hexSum))
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	var rule thirdparty.App
	for _, a := range m.For("windows") {
		if a.ID == "temurin-11-jdk" {
			rule = a
		}
	}
	c := &winget.Client{HTTP: srv.Client(), RawURL: srv.URL, APIURL: srv.URL}
	f := &pkgcache.Fetcher{HTTP: srv.Client()}
	for _, caso := range []struct {
		args, want string
	}{{rule.Install.Arguments, "/quiet /norestart"}, {"", "/quiet /norestart ADDLOCAL=ALL"}} {
		r := rule
		in := *rule.Install
		in.Arguments = caso.args
		r.Install = &in
		st := store.NewMemory()
		var out bytes.Buffer
		if !One(context.Background(), c, f, st, r, Options{Version: "11.0.33.10", Arch: "x64"}, t.TempDir(), &out) {
			t.Fatalf("fetch: %s", out.String())
		}
		pkgs, _ := st.ThirdPartyPackages(context.Background())
		if len(pkgs) != 1 || pkgs[0].Arguments != caso.want || pkgs[0].SHA256 != hexSum {
			t.Errorf("regra com %q: %+v", caso.args, pkgs)
		}
	}
}
