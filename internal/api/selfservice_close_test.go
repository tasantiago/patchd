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

// "Fechar o programa e instalar" na Central: o pedido adiado por programa aberto ganha o botão
// (com o agente que sabe fechar a pedido do usuário); pedir com close vira o job que fecha só o
// programa da sessão de quem pediu, sem aviso.
func TestCentralFecharEInstalar(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(thirdparty.NewLive(m)))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{{Name: "Microsoft Visual Studio Code", Version: "1.123.0", Publisher: "Microsoft Corporation", Scope: "machine"}}})
	inst := []byte(strings.Repeat("vs", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "vscode", Version: "1.141.0", Type: "inno", Architecture: "x64",
		SHA256: sha, Size: int64(len(inst)), Arguments: "/VERYSILENT"})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "vscode", Version: "1.141.0", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "vscode", CanUpdate: true, UpdatedBy: "thyago"})
	_ = st.SetMachineCapabilities(ctx, maq.MachineID, []string{protocol.CapabilityJobs, protocol.CapabilityCloseByUser})
	u := protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}, SID: "S-1-5-21-10-20-30-1001"}
	pede := func(close bool) protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: u, App: "vscode", Action: "atualizar", Close: close})
		var r protocol.SelfServiceResponse
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body).Body.Bytes(), &r)
		return r
	}
	loja := func() protocol.SelfServiceCatalog {
		body, _ := json.Marshal(u)
		var c protocol.SelfServiceCatalog
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body).Body.Bytes(), &c)
		return c
	}
	r := pede(false)
	if r.Status != protocol.SelfServiceCreated {
		t.Fatalf("pedido: %+v", r)
	}
	if c := loja(); len(c.Requests) != 1 || c.Requests[0].CloseAction != "" {
		t.Errorf("em andamento, sem o botão: %+v", c.Requests)
	}
	st.FinishJob(ctx, r.JobID, jobs.StatePostponed, "o instalador não chegou a rodar: tentar depois: programa aberto (1 Code.exe na sessão 2)", time.Now())
	c := loja()
	if len(c.Requests) != 1 || c.Requests[0].CloseAction != "atualizar" {
		t.Fatalf("adiado por programa aberto, com o botão: %+v", c.Requests)
	}
	r = pede(true)
	if r.Status != protocol.SelfServiceCreated || !strings.Contains(r.Message, "vai fechar Visual Studio Code") {
		t.Fatalf("fechar e instalar: %+v", r)
	}
	j, _, _ := st.JobForMachine(ctx, maq.MachineID, r.JobID)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	p, err := jobs.ParseThirdPartyParams(payload.Params)
	if err != nil || p.Close == nil || p.Close.User != u.Account || p.Close.UserSID != u.SID || p.Close.NoticeMinutes != 0 {
		t.Errorf("job: %+v %v", p, err)
	}
	// O agente sem a capacidade: o botão some, e o pedido com close é recusado.
	_, _ = st.CancelJob(ctx, r.JobID, "teste")
	_ = st.SetMachineCapabilities(ctx, maq.MachineID, []string{protocol.CapabilityJobs})
	if r := pede(true); r.Status != protocol.SelfServiceRefused || !strings.Contains(r.Message, "não consegue fechar") {
		t.Errorf("sem a capacidade: %+v", r)
	}
}

// A tela do autoatendimento mostra o nome de exibição de quem pediu (o do AD, dos conectados
// nas máquinas) e a conta embaixo; sem o nome, só a conta.
func TestAutoatendimentoComONomeDoUsuario(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithThirdPartyManifest(thirdparty.NewLive(m)))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	_ = st.SaveHostState(ctx, maq.MachineID, &protocol.MachineState{Users: []protocol.LoggedUser{
		{Account: `EXEMPLO\ana.souza`, DisplayName: "Ana Souza", Session: protocol.SessionConsole, Active: true}}}, "192.0.2.1")
	for _, who := range []string{`EXEMPLO\ana.souza`, `EXEMPLO\outro`} {
		raw, _ := json.Marshal(jobs.ThirdPartyParams{App: "7zip", Version: "26.04", SHA256: strings.Repeat("a", 64), Size: 1, Installer: "exe", Args: []string{"/S"}})
		if _, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: maq.MachineID, Type: jobs.TypeThirdParty, TTL: time.Hour,
			By: "autoatendimento: " + who, Params: raw}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	b := pagina(h, "GET", "/painel/autoatendimento", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<td>Ana Souza<br><span class="muted">(EXEMPLO\ana.souza)</span></td>`) || !strings.Contains(b, `<td>EXEMPLO\outro</td>`) {
		t.Errorf("tela:\n%s", b)
	}
}

// A lista de pedidos do autoatendimento: 50 por página (anterior/próxima), o filtro pela
// situação do job (com quantos há) e a busca por usuário, nome, máquina ou programa.
func TestAutoatendimentoFiltroEPaginas(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithThirdPartyManifest(thirdparty.NewLive(m)))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	_ = st.SaveHostState(ctx, maq.MachineID, &protocol.MachineState{Users: []protocol.LoggedUser{
		{Account: `EXEMPLO\ana.souza`, DisplayName: "Ana Souza", Session: protocol.SessionConsole, Active: true}}}, "192.0.2.1")
	raw, _ := json.Marshal(jobs.ThirdPartyParams{App: "7zip", Version: "26.04", SHA256: strings.Repeat("a", 64), Size: 1, Installer: "exe", Args: []string{"/S"}})
	for i := range 60 {
		who := `EXEMPLO\outro`
		if i < 3 {
			who = `EXEMPLO\ana.souza`
		}
		rec, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: maq.MachineID, Type: jobs.TypeThirdParty, TTL: time.Hour,
			By: "autoatendimento: " + who, Params: raw}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			st.FinishJob(ctx, rec.ID, jobs.StateFailed, "código 1603", time.Now())
		}
	}
	linhas := func(b string) int { return strings.Count(b, `<span class="job `) }
	b := pagina(h, "GET", "/painel/autoatendimento", sessaoAdmin, nil).Body.String()
	if linhas(b) != 50 || !strings.Contains(b, "1–50 de 60") || !strings.Contains(b, "pagina=2") || !strings.Contains(b, `<option value="falhou">falhou (6)</option>`) {
		t.Fatalf("primeira página: %d linhas", linhas(b))
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=30&pagina=2", sessaoAdmin, nil).Body.String(); linhas(b) != 10 || !strings.Contains(b, "51–60 de 60") {
		t.Errorf("segunda página: %d", linhas(b))
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=30&situacao=falhou", sessaoAdmin, nil).Body.String(); linhas(b) != 6 || strings.Contains(b, ">pendente</span>") {
		t.Errorf("só as falhas: %d", linhas(b))
	}
	// A busca acha pelo nome de exibição (e pela conta).
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=30&q=souza", sessaoAdmin, nil).Body.String(); linhas(b) != 3 || !strings.Contains(b, "Limpar") {
		t.Errorf("busca: %d", linhas(b))
	}
	if b := pagina(h, "GET", "/painel/autoatendimento?dias=30&q=7-zip&por=25", sessaoAdmin, nil).Body.String(); linhas(b) != 25 || !strings.Contains(b, "1–25 de 60") {
		t.Errorf("programa, 25 por página: %d", linhas(b))
	}
}
