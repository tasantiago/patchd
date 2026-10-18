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
)

// Aula 18.3: a oferta com aprovação da TI — o pedido espera, o admin aprova (vira job, com o
// usuário como autor) ou recusa com o motivo, e o usuário acompanha em Meus pedidos.
func TestAutoatendimentoComAprovacao(t *testing.T) {
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

	// A oferta pelo painel, com a caixa "Aprovação da TI".
	f := url.Values{"app": {"7zip"}, "atualizar": {"1"}, "aprovacao": {"1"}}
	if r := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, f); r.Code != http.StatusSeeOther {
		t.Fatalf("oferta: %d %s", r.Code, r.Body)
	}
	if l, _ := st.SelfServiceOffers(ctx); len(l) != 1 || !l[0].NeedsApproval {
		t.Fatalf("oferta gravada: %+v", l)
	}
	ana := protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{`EXEMPLO\GG-Exemplo`}}
	loja := func() protocol.SelfServiceCatalog {
		t.Helper()
		body, _ := json.Marshal(ana)
		var c protocol.SelfServiceCatalog
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body).Body.Bytes(), &c)
		return c
	}
	pede := func() protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: ana, App: "7zip", Action: "atualizar"})
		var r protocol.SelfServiceResponse
		_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body).Body.Bytes(), &r)
		return r
	}
	if c := loja(); len(c.Items) != 1 || !c.Items[0].Approval || c.Items[0].Pending != 0 {
		t.Fatalf("loja antes: %+v", c.Items)
	}

	// O pedido fica com a TI; nenhum job.
	r := pede()
	if r.Status != protocol.SelfServiceApproval || r.RequestID == 0 || r.JobID != 0 || !strings.Contains(r.Message, "aprovação da TI") {
		t.Fatalf("pedido: %+v", r)
	}
	if l, _ := st.Jobs(ctx, maq.MachineID, 10); len(l) != 0 {
		t.Fatalf("job antes da aprovação: %+v", l)
	}
	if r2 := pede(); r2.Status != protocol.SelfServiceWait || r2.RequestID != r.RequestID {
		t.Errorf("segundo pedido: %+v", r2)
	}
	c := loja()
	if c.Items[0].Pending != r.RequestID || len(c.Requests) != 1 || c.Requests[0].Request != r.RequestID || c.Requests[0].State != "aguardando aprovação" {
		t.Errorf("loja com o pedido esperando: %+v %+v", c.Items, c.Requests)
	}

	// No painel: a fila, com os botões só para o admin.
	b := pagina(h, "GET", "/painel/autoatendimento", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `id="aprovacao"`) || !strings.Contains(b, "7-Zip 26.04") || !strings.Contains(b, "/pedidos/1/aprovar") {
		t.Errorf("fila no painel:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/autoatendimento", sessaoTeste, nil).Body.String(); strings.Contains(b, "/aprovar") || !strings.Contains(b, `id="aprovacao"`) {
		t.Error("leitura vê a fila, sem os botões")
	}
	if w := pagina(h, "POST", "/painel/autoatendimento/pedidos/1/aprovar", sessaoTeste, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("leitura aprovando: %d", w.Code)
	}

	// Aprovado: job thirdparty com a ana como autora; a decisão na auditoria.
	w := pagina(h, "POST", "/painel/autoatendimento/pedidos/1/aprovar", sessaoAdmin, url.Values{})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "msg=aprovado&job=") {
		t.Fatalf("aprovar: %d %s", w.Code, w.Body)
	}
	l, _ := st.Jobs(ctx, maq.MachineID, 10)
	if len(l) != 1 || l[0].Type != jobs.TypeThirdParty || l[0].CreatedBy != `autoatendimento: EXEMPLO\ana` {
		t.Fatalf("job da aprovação: %+v", l)
	}
	if b := pagina(h, "GET", w.Header().Get("Location"), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Pedido aprovado: job") || strings.Contains(b, `id="aprovacao"`) {
		t.Errorf("depois de aprovar:\n%s", b)
	}
	if w := pagina(h, "POST", "/painel/autoatendimento/pedidos/1/aprovar", sessaoAdmin, url.Values{}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "já foi decidido") {
		t.Errorf("aprovar de novo: %d", w.Code)
	}
	if c := loja(); len(c.Requests) != 1 || c.Requests[0].ID != l[0].ID || c.Requests[0].Request != 0 {
		t.Errorf("o aprovado aparece pelo job: %+v", c.Requests)
	}

	// Outro pedido, recusado com o motivo (o job aberto não impede pedir para a TI).
	_, _ = st.FinishJob(ctx, l[0].ID, jobs.StateFailed, "x", time.Now().UTC())
	r = pede()
	if r.Status != protocol.SelfServiceApproval {
		t.Fatalf("segundo pedido: %+v", r)
	}
	w = pagina(h, "POST", "/painel/autoatendimento/pedidos/2/recusar", sessaoAdmin, url.Values{"motivo": {"use o 7-Zip da imagem padrão"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("recusar: %d", w.Code)
	}
	c = loja()
	if len(c.Requests) != 2 || c.Requests[0].State != "recusado pela TI" || c.Requests[0].Detail != "motivo: use o 7-Zip da imagem padrão" {
		t.Errorf("recusado em Meus pedidos: %+v", c.Requests)
	}

	// Um pedido que não vale mais (a oferta saiu): a aprovação não passa e o pedido segue aguardando.
	r = pede()
	_, _ = st.DeleteSelfServiceOffer(ctx, "7zip")
	w = pagina(h, "POST", "/painel/autoatendimento/pedidos/3/aprovar", sessaoAdmin, url.Values{})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "não vale mais") {
		t.Errorf("aprovar sem oferta: %d", w.Code)
	}
	if q, _, _ := st.SelfServiceRequestByID(ctx, r.RequestID); q.State != store.RequestWaiting {
		t.Errorf("continua aguardando: %+v", q)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."})
	acoes := map[string]int{}
	for _, e := range ev {
		acoes[e.Action]++
	}
	if acoes["autoatendimento.solicitacao"] != 3 || acoes["autoatendimento.aprovar"] != 1 || acoes["autoatendimento.reprovar"] != 1 {
		t.Errorf("auditoria: %v", acoes)
	}
}
