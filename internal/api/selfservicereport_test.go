package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Aula 17.7: Automação › Autoatendimento — os pedidos, como terminaram, e as recusas.
func TestPainelDoAutoatendimento(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64", Hostname: "pc-lab-01"},
		Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	inst := []byte(strings.Repeat("7z", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64",
		SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "7zip", CanUpdate: true, UpdatedBy: "thyago"})
	pede := func(acao string) protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}},
			App: "7zip", Action: acao})
		var resp protocol.SelfServiceResponse
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body).Body.Bytes(), &resp)
		return resp
	}
	if r := pede("instalar"); r.Status != protocol.SelfServiceRefused {
		t.Fatalf("recusa: %+v", r)
	}
	r := pede("atualizar")
	if r.Status != protocol.SelfServiceCreated {
		t.Fatalf("pedido: %+v", r)
	}
	// O job não deu certo; um job da TI na mesma máquina não entra na página.
	_, _ = st.FinishJob(ctx, r.JobID, jobs.StateFailed, "o instalador terminou com o código 1603", time.Now().UTC())
	if _, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: maq.MachineID, Type: jobs.TypeRescan, TTL: time.Hour, By: "thyago"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	w := pagina(h, "GET", "/painel/autoatendimento", sessaoTeste, nil) // o perfil de leitura também vê
	b := w.Body.String()
	for _, quer := range []string{`class="ativa" aria-current="page">Autoatendimento</a>`, `EXEMPLO\ana`, "7-Zip 26.04", "pc-lab-01", "código 1603",
		`<span class="kpi-rot">Não deram certo</span><span class="kpi-val v-vermelho">1</span>`, "1 de 1 nos últimos 30 dias",
		"7zip · instalar · recusado", `<td>7-Zip</td><td class="num">1</td>`} {
		if !strings.Contains(b, quer) {
			t.Errorf("sem %q", quer)
		}
	}
	if strings.Contains(b, "buscar atualizações") || w.Code != 200 {
		t.Errorf("o job da TI não é pedido do usuário (%d)", w.Code)
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?estado=concluido", sessaoTeste, nil).Body.String(); strings.Contains(b, "código 1603") || !strings.Contains(b, "0 de 1") {
		t.Error("filtro por estado")
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=7&estado=falha", sessaoTeste, nil).Body.String(); !strings.Contains(b, "código 1603") || !strings.Contains(b, "<b>7 dias</b>") {
		t.Error("período e filtro")
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=9999", sessaoTeste, nil).Body.String(); !strings.Contains(b, "<b>30 dias</b>") {
		t.Error("período fora da lista volta ao padrão")
	}
	if w := pagina(h, "GET", "/painel/autoatendimento", "", nil); w.Code == 200 {
		t.Error("sem sessão, não")
	}
}
