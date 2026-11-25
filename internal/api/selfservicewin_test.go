package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// As atualizações do Windows pela Central: só as aprovadas em Patch e pendentes na busca da
// máquina; o pedido cria o mesmo job do painel, em nome do usuário, e nunca reinicia.
func TestAtualizacoesDoWindowsPelaCentral(t *testing.T) {
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	ctx := context.Background()
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	src := &complianceFalso{}
	params := settings.New([]settings.Def{{Key: api.SelfServiceWindowsKey, Group: "Agentes", Label: "Windows na Central", Default: "ligado",
		Check: func(v string) (string, error) { return v, nil }}}, func(string) (string, bool) { return "", false }, store.NewMemory(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithCompliance(src), api.WithThirdPartyManifest(thirdparty.NewLive(m)), api.WithSettings(params))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	id := maq.MachineID
	inventario := func(name string) {
		_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: name, OS: protocol.OSInfo{ID: "windows", Name: name, Arch: "amd64"}})
	}
	inventario("Windows 11 Pro")
	fix := "26300.9711 (KB5127648)"
	agora := time.Now().UTC()
	src.fleet = protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: id, State: "faltando", Pending: 2, LastSeenAt: agora, EvaluatedAt: agora}}}
	src.itens = map[string][]protocol.PendingItem{id: {{ID: "CVE-2026-1001", FixedIn: fix, Exploited: true}, {ID: "CVE-2026-3001", FixedIn: "26300.9800 (KB5130001)"}}}
	_ = st.SaveScan(ctx, id, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: agora,
		Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{
			{ID: cu, KBs: []string{"5127648"}, Title: "Atualização cumulativa de 2026-11", RestartRequired: true},
			{ID: "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", KBs: []string{"5130001"}, Title: "Outra"}}}}})
	user := protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}}
	loja := func() protocol.SelfServiceCatalog {
		t.Helper()
		body, _ := json.Marshal(user)
		r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
		if r.Code != http.StatusOK {
			t.Fatalf("loja: %d %s", r.Code, r.Body)
		}
		var c protocol.SelfServiceCatalog
		_ = json.Unmarshal(r.Body.Bytes(), &c)
		return c
	}
	pede := func(app string) protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: user, App: app, Action: protocol.SelfServiceWindows})
		r := envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body)
		if r.Code != http.StatusOK {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		var resp protocol.SelfServiceResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}

	// 1. Nada aprovado: a seção existe, sem atualização para instalar, com as que esperam a TI.
	w := loja().Windows
	if w == nil || len(w.Updates) != 0 || w.Waiting != 2 {
		t.Fatalf("sem aprovação: %+v", w)
	}
	if r := pede(""); r.Status != protocol.SelfServiceRefused {
		t.Fatalf("pedido sem aprovação: %+v", r)
	}

	// 2. Uma CVE do KB5127648 aprovada: ele aparece, com o título da busca e o aviso de reinício.
	_, _ = st.ApprovePatch(ctx, store.PatchApproval{Item: "CVE-2026-1001", FixedIn: fix, ApprovedBy: "thyago", ApprovedAt: agora})
	w = loja().Windows
	if w == nil || len(w.Updates) != 1 || w.Updates[0].KB != "5127648" || w.Updates[0].Title != "Atualização cumulativa de 2026-11" ||
		!w.Updates[0].Restart || !w.Updates[0].Exploited || w.Waiting != 1 {
		t.Fatalf("aprovado: %+v", w)
	}
	// Pedido com programa: inválido.
	if r := pede("7zip"); r.Status != protocol.SelfServiceRefused {
		t.Fatalf("windows com programa: %+v", r)
	}
	r := pede("")
	if r.Status != protocol.SelfServiceCreated || r.JobID == 0 || !strings.Contains(r.Message, "não reinicia sozinho") {
		t.Fatalf("pedido: %+v", r)
	}
	env, _ := st.DeliverJobs(ctx, id, time.Now().UTC())
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	j, err := jobs.Verify(priv.Public().(ed25519.PublicKey), env[0], id, time.Now().UTC(), 0)
	p, _ := jobs.ParseWinUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeWinUpdate || len(p.Updates) != 1 || p.Updates[0].ID != cu {
		t.Fatalf("job: %+v %+v %v", j, p, err)
	}
	rows, _ := st.Jobs(ctx, id, 5)
	if len(rows) != 1 || rows[0].CreatedBy != `autoatendimento: EXEMPLO\ana` {
		t.Fatalf("autor: %+v", rows)
	}
	// 3. Com o job em aberto: a Central mostra o andamento; outro pedido espera; Meus pedidos lista.
	c := loja()
	if c.Windows.JobID != r.JobID {
		t.Fatalf("andamento: %+v", c.Windows)
	}
	if r2 := pede(""); r2.Status != protocol.SelfServiceWait || r2.JobID != r.JobID {
		t.Fatalf("segundo pedido: %+v", r2)
	}
	if len(c.Requests) == 0 || c.Requests[0].Name != "Atualizações do Windows" || c.Requests[0].Version != "KB5127648" {
		t.Fatalf("Meus pedidos: %+v", c.Requests)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento.pedido"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "atualizações do Windows KB5127648") {
		t.Fatalf("auditoria: %+v", ev)
	}

	// 4. Em servidor, fica com a TI; com o parâmetro desligado, a seção some.
	inventario("Windows Server 2025 Standard")
	if w := loja().Windows; w != nil {
		t.Fatalf("servidor: %+v", w)
	}
	inventario("Windows 11 Pro")
	if _, err := params.Apply(ctx, map[string]string{api.SelfServiceWindowsKey: "desligado"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if w := loja().Windows; w != nil {
		t.Fatalf("desligado: %+v", w)
	}
	if r := pede(""); r.Status != protocol.SelfServiceRefused {
		t.Fatalf("pedido desligado: %+v", r)
	}
}
