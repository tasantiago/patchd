package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Aula 16.3: na aba Pendências do Windows, as correções por KB e o botão que instala os
// KBs aprovados que a busca da máquina mostra pendentes.
func TestAtualizacoesDoWindowsPeloPainel(t *testing.T) {
	const maq, cu = "92fc3b93-0003", "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	agora := time.Now().UTC()
	st := store.NewMemory()
	ctx := context.Background()
	abreSessao(t, st, sessaoTeste, agora)
	_ = st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	_, _ = st.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: "x", OS: protocol.OSInfo{Hostname: "vm-win"}})
	fix, net := "26300.9711 (KB5127648)", "26300.9711 (KB5130001)"
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{
			{MachineID: maq, Hostname: "vm-win", OS: "Windows 11 Pro 26H2", State: "faltando", Pending: 4, PendingUnit: "CVEs", LastSeenAt: agora, EvaluatedAt: agora}}},
		itens: map[string][]protocol.PendingItem{maq: {
			{ID: "CVE-2026-1001", FixedIn: fix, Exploited: true}, {ID: "CVE-2026-1002", FixedIn: fix}, {ID: "CVE-2026-1003", FixedIn: fix},
			{ID: "CVE-2026-2001", FixedIn: net},
		}},
	}
	_, priv, _ := jobs.GenerateKey()
	h := api.New(st, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), api.WithCompliance(src), api.WithJobKey(priv))
	busca := func(wsus bool) {
		def := protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}, Source: "wua-default"}}}
		if !wsus {
			def = protocol.ScanResult{Source: "wua-default", Error: "Search: HRESULT 0x80244007"}
		}
		_ = st.SaveScan(ctx, maq, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
			Scans: []protocol.ScanResult{def, {Source: "wua-offline", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}, {ID: "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", KBs: []string{"5130001"}}}}}})
	}
	busca(true)
	aba := "/painel/maquinas/" + maq + "?aba=pendencias"
	post := func() string {
		return pagina(h, "POST", "/painel/maquinas/"+maq+"/windows/instalar", sessaoAdmin, url.Values{"quando": {"agora"}}).Header().Get("Location")
	}

	// 1. Nada aprovado: o KB aparece, mas sem botão; o POST não cria nada.
	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Atualizações do Windows pelo patchd", "<code>KB5127648</code>", "pendente na fonte", "nenhuma CVE desta correção aprovada em Patch",
		"<code>KB5130001</code>", "só no catálogo offline"} {
		if !strings.Contains(b, tr) {
			t.Errorf("sem aprovação: faltou %q", tr)
		}
	}
	if strings.Contains(b, "/windows/instalar") {
		t.Error("sem aprovação não há botão")
	}
	if loc := post(); !strings.HasSuffix(loc, "erro=windows-nada") {
		t.Errorf("POST sem aprovação: %s", loc)
	}

	// 2. Duas CVEs do KB aprovadas: botão com o KB; o leitor não vê o botão.
	for _, c := range []string{"CVE-2026-1001", "CVE-2026-1002"} {
		_, _ = st.ApprovePatch(ctx, store.PatchApproval{Item: c, FixedIn: fix, ApprovedBy: "thyago", ApprovedAt: agora})
	}
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<td class="num">2 <span class="expl">1 explorada(s)</span></td>`) || !strings.Contains(b, ">Instalar KB5127648</button>") {
		t.Error("aprovado: faltou a contagem ou o botão")
	}
	if strings.Contains(pagina(h, "GET", aba, sessaoTeste, nil).Body.String(), "/windows/instalar") {
		t.Error("leitura não vê o botão")
	}

	// 3. O POST cria o job com o UpdateID da busca; um segundo pedido esbarra no aberto.
	loc := post()
	if !strings.Contains(loc, "criado=1&job=") {
		t.Fatalf("POST: %s", loc)
	}
	env, _ := st.DeliverJobs(ctx, maq, time.Now().UTC())
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	j, err := jobs.Verify(priv.Public().(ed25519.PublicKey), env[0], maq, time.Now().UTC(), 0)
	p, _ := jobs.ParseWinUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeWinUpdate || len(p.Updates) != 1 || p.Updates[0].ID != cu || p.Updates[0].KB != "5127648" {
		t.Errorf("job: %+v %+v %v", j, p, err)
	}
	if loc := post(); !strings.Contains(loc, "erro=windows-aberto&job=") {
		t.Errorf("segundo pedido: %s", loc)
	}
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Instalação em aberto:") || strings.Contains(b, "/windows/instalar") {
		t.Error("com job em aberto: aviso sem botão")
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "job.criar"}); len(ev) != 1 || !strings.Contains(ev[0].Detail, "instalar atualizações do Windows · KB5127648") {
		t.Errorf("auditoria: %+v", ev)
	}

	// 4. A fonte da política falhou (Aula 16.2): aviso no cartão, sem botão, POST recusado.
	_, _ = st.FinishJob(ctx, j.ID, jobs.StateDone, "ok", time.Now().UTC())
	busca(false)
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "sem busca válida") || strings.Contains(b, "/windows/instalar") {
		t.Error("fonte falhando: faltou o estado, ou sobrou o botão")
	}
	if loc := post(); !strings.HasSuffix(loc, "erro=windows-fonte") {
		t.Errorf("POST com a fonte falhando: %s", loc)
	}
}
