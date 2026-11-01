package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func standardForm() url.Values {
	return url.Values{"id": {"0"}, "revisao": {"0"}, "nome": {"Financeiro"}, "ativo": {"1"}, "maquina": {"60ef97e4-0001"}, "grupos_ad": {`CORP\Financeiro`}, "atalho_nome": {"Portal"}, "atalho_tipo": {"site"}, "atalho_destino": {"https://financeiro.example"}, "atalho_icone": {"site"}, "atalho_os": {""}}
}

func TestStandardizationScriptScheduledOnCapableAgentCheckin(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, st, _ := ambientePaginas(t, api.WithJobKey(key), api.WithContent(t.TempDir(), nil))
	ctx := context.Background()
	uploaded := sobePacote(h, sessaoAdmin, "financeiro", "instalar.sh", []byte("#!/bin/sh\nexit 0\n"), url.Values{"entrada": {"instalar.sh"}})
	if uploaded.Code != 303 || strings.Contains(uploaded.Header().Get("Location"), "erro=") {
		t.Fatalf("upload: %d %s", uploaded.Code, uploaded.Header().Get("Location"))
	}
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	if _, err := st.SaveInventory(ctx, m.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:" + strings.Repeat("a", 64), OS: protocol.OSInfo{Family: "linux", ID: "ubuntu", Arch: "amd64"}, Software: []protocol.Software{}}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"nome": {"Financeiro"}, "ativo": {"1"}, "todos": {"1"}, "instalador_pacote": {"1"}, "instalador_os": {"linux"}, "instalador_software": {"app-financeiro"}}
	if r := pagina(h, "POST", "/painel/padronizacao", sessaoAdmin, form); r.Code != 303 {
		t.Fatalf("salvar instalador: %d %s", r.Code, r.Body)
	}
	bundles, err := st.ScriptBundles(ctx)
	if err != nil || len(bundles) != 1 {
		t.Fatalf("pacote: %v %v", bundles, err)
	}
	// Mudar parâmetros no cadastro não altera a aprovação já fixada no grupo.
	if _, err := st.SetScriptExecution(ctx, bundles[0].ID, "instalar.sh", []string{"novo"}, 300); err != nil {
		t.Fatal(err)
	}
	check := protocol.CheckinRequest{AgentVersion: "teste", InventoryHash: "sha256:" + strings.Repeat("a", 64), SentAt: time.Now(), Capabilities: []string{protocol.CapabilityJobs}}
	raw, _ := json.Marshal(check)
	if r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, raw); r.Code != 200 {
		t.Fatal(r.Code)
	}
	list, _ := st.Jobs(ctx, m.MachineID, 10)
	if len(list) != 0 {
		t.Fatal("agente sem capacidade recebeu nova política")
	}
	check.Capabilities = append(check.Capabilities, protocol.CapabilityStandardization)
	raw, _ = json.Marshal(check)
	r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, raw)
	if r.Code != 200 {
		t.Fatalf("check-in: %d %s", r.Code, r.Body)
	}
	var response protocol.CheckinResponse
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Jobs) != 1 {
		t.Fatalf("jobs: %s", r.Body)
	}
	job, err := jobs.Verify(pub, response.Jobs[0], m.MachineID, time.Now(), 0)
	if err != nil || job.Type != jobs.TypeScript {
		t.Fatalf("assinatura: %+v %v", job, err)
	}
	params, err := jobs.ParseScriptParams(job.Params)
	if err != nil || len(params.Args) != 0 || params.SHA256 != bundles[0].SHA256 {
		t.Fatalf("aprovação mudou: %+v %v", params, err)
	}
}

func TestStandardizationPanelCRUDAndAuthorization(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	path := "/painel/padronizacao"
	if r := pagina(h, "GET", path, sessaoAdmin, nil); r.Code != 200 || !strings.Contains(r.Body.String(), "instalador_software") {
		t.Fatalf("página: %d %s", r.Code, r.Body)
	}
	if r := pagina(h, "GET", path, sessaoTeste, nil); r.Code != 403 {
		t.Fatal(r.Code)
	}
	form := standardForm()
	if r := pagina(h, "POST", path, sessaoAdmin, form, "Origin", "https://externo.example"); r.Code != 403 {
		t.Fatalf("origem externa: %d", r.Code)
	}
	if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != 303 {
		t.Fatalf("salvar: %d %s", r.Code, r.Body)
	}
	policies, err := st.StandardizationPolicies(context.Background())
	if err != nil || len(policies) != 1 || !policies[0].Enabled || len(policies[0].ADGroups) != 1 || len(policies[0].Machines) != 1 {
		t.Fatalf("destinos: %+v %v", policies, err)
	}
	form.Set("id", "1")
	form.Set("revisao", "1")
	form.Set("nome", "<script>alert(1)</script>")
	if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != 303 {
		t.Fatalf("editar: %d %s", r.Code, r.Body)
	}
	if r := pagina(h, "GET", path+"?grupo=1", sessaoAdmin, nil); r.Code != 200 || strings.Contains(r.Body.String(), "<script>alert(1)</script>") {
		t.Fatalf("escape: %d %s", r.Code, r.Body)
	}
	if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != 409 {
		t.Fatalf("edição obsoleta: %d", r.Code)
	}
	if r := pagina(h, "POST", path+"/1/excluir", sessaoAdmin, url.Values{"revisao": {"1"}}); r.Code != 409 {
		t.Fatalf("exclusão obsoleta: %d", r.Code)
	}
	if r := pagina(h, "POST", path+"/1/excluir", sessaoAdmin, url.Values{"revisao": {"2"}}); r.Code != 303 {
		t.Fatalf("excluir: %d %s", r.Code, r.Body)
	}
	policies, _ = st.StandardizationPolicies(context.Background())
	if len(policies) != 0 {
		t.Fatal("grupo não excluído")
	}
}

func TestStandardizationAgentPlanAndStaleReports(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "padrão", time.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	if _, err := st.SaveInventory(ctx, m.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "standard", OS: protocol.OSInfo{Family: "linux", ID: "ubuntu"}, Software: []protocol.Software{}}); err != nil {
		t.Fatal(err)
	}
	p := store.StandardizationPolicy{Name: "RH", Enabled: true, ADGroups: []string{`CORP\RH`}, Shortcuts: []protocol.StandardShortcut{{Name: "Portal", Kind: "site", Target: "https://rh.example"}}}
	if _, err := st.SaveStandardizationPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	user := protocol.SelfServiceUser{Account: `CORP\Ana`, Groups: []string{`CORP\RH`}}
	raw, _ := json.Marshal(user)
	r := envia(h, "POST", protocol.StandardizationPath, m.Credential, raw)
	if r.Code != 200 {
		t.Fatalf("plano: %d %s", r.Code, r.Body)
	}
	var plan protocol.StandardizationPlan
	if err := json.Unmarshal(r.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Shortcuts) != 1 || len(plan.Policies) != 1 {
		t.Fatalf("grupo AD: %+v", plan)
	}
	report := protocol.StandardizationReport{SelfServiceUser: user, StandardizationResult: protocol.StandardizationResult{Revision: plan.Revision, Items: []protocol.StandardShortcutResult{{Name: "Portal", State: "aplicado"}}}}
	raw, _ = json.Marshal(report)
	if r := envia(h, "POST", protocol.StandardizationReportPath, m.Credential, raw); r.Code != 200 {
		t.Fatalf("relato: %d %s", r.Code, r.Body)
	}
	report.Revision = strings.Repeat("b", 64)
	raw, _ = json.Marshal(report)
	if r := envia(h, "POST", protocol.StandardizationReportPath, m.Credential, raw); r.Code != http.StatusConflict {
		t.Fatalf("revisão: %d %s", r.Code, r.Body)
	}
	if r := envia(h, "POST", protocol.StandardizationPath, "", raw); r.Code != 401 {
		t.Fatalf("sem credencial: %d", r.Code)
	}
}
