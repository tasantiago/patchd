package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// O programa que não informa a versão (o PJeOffice Pro: "Auto Updatable"): incluído pelo
// painel com a opção, a Central oferece instalar a quem não tem e mostra "instalado" a quem
// tem; a avaliação e o plano contam como em dia.
func TestProgramaSemVersao(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(live), api.WithCustomApps(base, nil, nil),
		api.WithCompliance(&complianceFalso{}))
	ctx := context.Background()
	form := url.Values{"origem": {"proprio"}, "nome": {"PJeOffice Pro"}, "inventario": {"PJeOffice Pro"}, "modo": {"igual"},
		"fabricante": {"Conselho Nacional de Justiça - CNJ"}, "tipo": {"inno"}, "assinatura": {"nao"}, "sem_assinatura": {"teste"}, "sem_versao": {"1"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("incluir: %d %s", r.Code, r.Body)
	}
	var rule thirdparty.App
	for _, a := range live.For("windows") {
		if a.ID == "pjeoffice-pro" {
			rule = a
		}
	}
	if !rule.NoVersion() {
		t.Fatalf("regra: %+v", rule.Install)
	}
	if b := pagina(h, "GET", "/painel/patch/programa?app=pjeoffice-pro", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não informa a versão</strong>") {
		t.Error("página do programa")
	}
	tem := []protocol.Software{{Name: "PJeOffice Pro", Version: "Auto Updatable", Publisher: "Conselho Nacional de Justiça - CNJ", Scope: "machine"}}
	// A avaliação e o plano: em dia.
	if r := thirdparty.Evaluate("windows", tem, live.Get(), nil); len(r.Findings) != 1 || r.Findings[0].State != thirdparty.StateCurrent {
		t.Errorf("avaliação: %+v", r.Findings)
	}
	if ds := tpinstall.Plan(tem, live.Get(), nil, "x64"); len(ds) != 1 || ds[0].State != tpinstall.StateCurrent {
		t.Errorf("plano: %+v", ds)
	}
	// A Central: sem o programa, instalar; com ele, instalado.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	inst := []byte(strings.Repeat("pj", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "pjeoffice-pro", Version: "2026.11.24", Type: "inno", Architecture: "x64",
		Scope: "machine", URL: store.UploadPrefix + "pjeoffice.exe", SHA256: sha, Size: int64(len(inst))})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "pjeoffice-pro", Version: "2026.11.24", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "pjeoffice-pro", CanInstall: true, CanUpdate: true, UpdatedBy: "thyago"})
	loja := func(sw []protocol.Software, hash string) protocol.SelfServiceItem {
		t.Helper()
		_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: hash,
			OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"}, Software: sw})
		body, _ := json.Marshal(protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}})
		var c protocol.SelfServiceCatalog
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body).Body.Bytes(), &c)
		for _, it := range c.Items {
			if it.App == "pjeoffice-pro" {
				return it
			}
		}
		t.Fatalf("sem o item: %+v", c.Items)
		return protocol.SelfServiceItem{}
	}
	if it := loja(nil, "a"); it.Action != "instalar" || it.Target != "2026.11.24" {
		t.Errorf("sem o programa: %+v", it)
	}
	if it := loja(tem, "b"); it.Action != "instalado" || it.Installed != "Auto Updatable" {
		t.Errorf("com o programa: %+v", it)
	}
}
