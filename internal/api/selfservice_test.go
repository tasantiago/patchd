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

// Aula 17.2: a loja e o pedido do usuário pela API do agente, com os grupos do token.
func TestAutoatendimentoPeloAgente(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)

	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	_ = st.SaveHostState(ctx, maq.MachineID, &protocol.MachineState{Users: []protocol.LoggedUser{{Account: `EXEMPLO\ana`, Session: protocol.SessionConsole, Active: true}}}, "192.0.2.1")
	inst := []byte(strings.Repeat("7z", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	if err := os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640); err != nil {
		t.Fatal(err)
	}
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64",
		SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "7zip", CanUpdate: true, Groups: []string{"GG-Exemplo"}, UpdatedBy: "thyago"})

	loja := func(groups ...string) protocol.SelfServiceCatalog {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: groups})
		r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
		if r.Code != http.StatusOK {
			t.Fatalf("loja: %d %s", r.Code, r.Body)
		}
		var c protocol.SelfServiceCatalog
		_ = json.Unmarshal(r.Body.Bytes(), &c)
		return c
	}
	pede := func(app, acao string) protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: protocol.SelfServiceUser{Account: `EXEMPLO\ana`,
			Groups: []string{`EXEMPLO\Domain Users`, `exemplo\gg-exemplo`}}, App: app, Action: acao})
		r := envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body)
		if r.Code != http.StatusOK {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		var resp protocol.SelfServiceResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}

	// Sem credencial, ou com conta fora do formato: recusado antes de tudo.
	if r := envia(h, "POST", protocol.SelfServiceCatalogPath, "", []byte(`{"account":"x","groups":[]}`)); r.Code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", r.Code)
	}
	if r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, []byte(`{"account":"a\\b\\c","groups":[]}`)); r.Code != http.StatusBadRequest {
		t.Errorf("conta ruim: %d", r.Code)
	}
	// Fora do grupo: loja vazia. No grupo (pelo token, com o domínio): atualizar.
	if c := loja(`EXEMPLO\GG-Outro`); len(c.Items) != 0 {
		t.Errorf("fora do grupo: %+v", c.Items)
	}
	c := loja(`EXEMPLO\Domain Users`, `EXEMPLO\GG-Exemplo`)
	if len(c.Items) != 1 || c.Items[0].App != "7zip" || c.Items[0].Action != "atualizar" || c.Items[0].Installed != "26.01" || c.Items[0].Target != "26.04" {
		t.Fatalf("no grupo: %+v", c)
	}

	// Instalar o que só pode ser atualizado: recusado, auditado.
	if r := pede("7zip", "instalar"); r.Status != protocol.SelfServiceRefused || !strings.Contains(r.Message, "não está disponível") {
		t.Errorf("ação errada: %+v", r)
	}
	// O pedido: job thirdparty assinado, com o usuário como autor.
	r := pede("7zip", "atualizar")
	if r.Status != protocol.SelfServiceCreated || r.JobID == 0 || !strings.Contains(r.Message, "atualização de 7-Zip 26.04") {
		t.Fatalf("pedido: %+v", r)
	}
	j, _, _ := st.JobForMachine(ctx, maq.MachineID, r.JobID)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	p, err := jobs.ParseThirdPartyParams(payload.Params)
	if err != nil || j.Type != jobs.TypeThirdParty || j.CreatedBy != `autoatendimento: EXEMPLO\ana` || p.App != "7zip" || p.Version != "26.04" || p.SHA256 != sha {
		t.Errorf("job: %+v %+v %v", j, p, err)
	}
	// Outro pedido com o job em aberto: aguarde; a loja mostra o job.
	if r2 := pede("7zip", "atualizar"); r2.Status != protocol.SelfServiceWait || r2.JobID != r.JobID {
		t.Errorf("em aberto: %+v", r2)
	}
	if c := loja(`EXEMPLO\GG-Exemplo`); c.OpenJob != r.JobID {
		t.Errorf("loja com job em aberto: %+v", c)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."})
	if len(ev) != 3 || ev[1].Action != "autoatendimento.pedido" || ev[1].Username != `EXEMPLO\ana` || ev[1].Source != "autoatendimento" ||
		!strings.Contains(ev[1].Detail, "7zip 26.04 · atualizar · visto conectado: sim") || ev[0].Action != "autoatendimento.recusado" {
		t.Errorf("auditoria: %+v", ev)
	}
}
