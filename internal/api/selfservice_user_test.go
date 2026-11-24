package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
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
)

// O programa por usuário na Central: a loja olha as instalações do perfil de quem pede (pelo
// SID); o pedido vira job com o usuário (o agente instala como ele); o agente que não sabe
// instalar no perfil deixa o item indisponível.
func TestLojaPorUsuario(t *testing.T) {
	base, _ := thirdparty.Default()
	canva, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Canva", WingetID: "Canva.Canva", InventoryName: "Canva", NameMode: thirdparty.NamePrefix,
		InstallType: "nullsoft", Signer: "Canva Pty Ltd"})
	if err != nil {
		t.Fatal(err)
	}
	canva.Install.PerUser = true
	m, err := thirdparty.ParseCustom(append(append([]thirdparty.App{}, base.Apps...), canva))
	if err != nil {
		t.Fatal(err)
	}
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(thirdparty.NewLive(m)))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	const ana, sidAna = `EXEMPLO\ana`, "S-1-5-21-10-20-30-1001"
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		// O Canva de outra pessoa: não conta para a ana.
		Software: []protocol.Software{{Name: "Canva 1.100.0", Version: "1.100.0", Publisher: "Canva", Scope: "user", User: "S-1-5-21-10-20-30-1002"}}})
	inst := []byte(strings.Repeat("cv", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	if err := os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640); err != nil {
		t.Fatal(err)
	}
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: canva.ID, Version: "1.120.0", Type: "nullsoft", Architecture: "x64", Scope: "user",
		SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: canva.ID, Version: "1.120.0", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: canva.ID, CanInstall: true, CanUpdate: true, UpdatedBy: "thyago"})
	u := protocol.SelfServiceUser{Account: ana, Groups: []string{}, SID: sidAna}
	loja := func() protocol.SelfServiceItem {
		t.Helper()
		body, _ := json.Marshal(u)
		r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
		var c protocol.SelfServiceCatalog
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &c) != nil || len(c.Items) != 1 {
			t.Fatalf("loja: %d %s", r.Code, r.Body)
		}
		return c.Items[0]
	}
	// O agente sem a capacidade: indisponível.
	if it := loja(); it.Action != "indisponivel" || !strings.Contains(it.Reason, "ainda não instala no seu perfil") {
		t.Errorf("sem a capacidade: %+v", it)
	}
	_ = st.SetMachineCapabilities(ctx, maq.MachineID, []string{protocol.CapabilityJobs, protocol.CapabilityUserInstall})
	if it := loja(); it.Action != "instalar" || it.Target != "1.120.0" || it.Installed != "" {
		t.Fatalf("instalar: %+v", it)
	}
	body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: u, App: canva.ID, Action: "instalar"})
	var resp protocol.SelfServiceResponse
	_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body).Body.Bytes(), &resp)
	if resp.Status != protocol.SelfServiceCreated {
		t.Fatalf("pedido: %+v", resp)
	}
	j, _, _ := st.JobForMachine(ctx, maq.MachineID, resp.JobID)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	p, err := jobs.ParseThirdPartyParams(payload.Params)
	if err != nil || p.User != ana || p.UserSID != sidAna || p.Version != "1.120.0" {
		t.Errorf("job: %+v %v", p, err)
	}
	_, _ = st.CancelJob(ctx, resp.JobID, "teste")
	// Com o Canva antigo no perfil dela: atualizar; na versão aprovada: em dia.
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w2",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{{Name: "Canva 1.100.0", Version: "1.100.0", Publisher: "Canva", Scope: "user", User: sidAna}}})
	if it := loja(); it.Action != "atualizar" || it.Installed != "1.100.0" || it.Target != "1.120.0" {
		t.Errorf("atualizar: %+v", it)
	}
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w3",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{{Name: "Canva 1.120.0", Version: "1.120.0", Publisher: "Canva", Scope: "user", User: sidAna}}})
	if it := loja(); it.Action != "em_dia" {
		t.Errorf("em dia: %+v", it)
	}
}
