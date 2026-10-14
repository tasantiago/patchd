package tpinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// A campanha de terceiros (Aula 14.7): a versão pronta a cada anel, e a decisão por máquina.
func TestCampaigner(t *testing.T) {
	ctx := context.Background()
	m, _ := thirdparty.Default()
	st := store.NewMemory()
	dir := t.TempDir()
	msiBytes := []byte("MSI do LibreOffice 26.8.1.1")
	h := sha256.Sum256(msiBytes)
	sum := hex.EncodeToString(h[:])
	if _, err := st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "libreoffice", WingetID: "TheDocumentFoundation.LibreOffice",
		Version: "26.8.1.1", Architecture: "x64", Type: "msi", URL: "https://x", SHA256: sum, Size: int64(len(msiBytes)), Arguments: "/quiet /norestart ALLUSERS=1"}); err != nil {
		t.Fatal(err)
	}
	c := Campaigner{Store: st, Manifest: m, Dir: dir}
	pronta := func(app, version, want string) {
		t.Helper()
		if r, err := c.Ready(ctx, app, version); err != nil || !strings.Contains(r, want) || (want == "" && r != "") {
			t.Errorf("%s %s: %q %v; esperava %q", app, version, r, err, want)
		}
	}
	pronta("libreoffice", "26.8.1.1", "não está aprovado")
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "libreoffice", Version: "26.8.1.1", ApprovedBy: "ana"})
	pronta("libreoffice", "26.8.1.1", "não confere no cache")
	os.WriteFile(filepath.Join(dir, sum+".msi"), msiBytes, 0o640)
	pronta("libreoffice", "26.8.1.1", "")
	pronta("vcredist-2008", "9.0", `não tem "install"`)
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "libreoffice", Version: "27.0", ApprovedBy: "ana"})
	pronta("libreoffice", "27.0", "nenhum instalador de libreoffice 27.0 guardado")
	if r, _ := (Campaigner{Store: st, Manifest: m}).Ready(ctx, "libreoffice", "26.8.1.1"); !strings.Contains(r, "PATCHD_CONTENT_DIR") {
		t.Errorf("sem pasta: %q", r)
	}
	os.WriteFile(filepath.Join(dir, sum+".msi"), []byte("trocado"), 0o640)
	pronta("libreoffice", "26.8.1.1", "não confere no cache")
	os.WriteFile(filepath.Join(dir, sum+".msi"), msiBytes, 0o640)

	lo := func(ver string, extra ...func(*protocol.Software)) protocol.Software {
		return maquina("LibreOffice "+ver, ver, "The Document Foundation", extra...)
	}
	win := func(id string, sw ...protocol.Software) {
		st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, OS: protocol.OSInfo{ID: "windows", Arch: "amd64"}, Software: sw})
	}
	win("antiga", lo("25.2.1.1", msi))
	win("nova", lo("26.8.1.1", msi))
	win("sem", maquina("Google Chrome", "155.0.8059.40", "Google LLC", msi))
	win("exe", lo("25.2.1.1"))
	win("dupla", lo("25.2.1.1", msi), lo("24.8.0.1"))
	st.SaveInventory(ctx, "linux", protocol.InventoryReport{SchemaVersion: 1, Hash: "linux", OS: protocol.OSInfo{ID: "ubuntu", Arch: "amd64"}})
	for id, want := range map[string]string{"nova": "em dia", "sem": "sem o programa", "exe": "sem pacote", "dupla": "duplicado",
		"linux": "sem inventário do Windows", "nenhuma": "sem inventário do Windows"} {
		if p, skip, err := c.Job(ctx, id, "libreoffice", "26.8.1.1"); err != nil || p != nil || skip != want {
			t.Errorf("%s: %+v %q %v; esperava %q", id, p, skip, err, want)
		}
	}
	p, skip, err := c.Job(ctx, "antiga", "libreoffice", "26.8.1.1")
	if err != nil || p == nil || skip != "" || p.Version != "26.8.1.1" || p.SHA256 != sum || p.Installer != "msi" ||
		p.Signer != "The Document Foundation" || strings.Join(p.Args, " ") != "/quiet /norestart ALLUSERS=1" {
		t.Errorf("antiga: %+v %q %v", p, skip, err)
	}
}
