package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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

// TestLigarPelaRede (docs/wake-on-lan.md, Aula 22.2): o admin pede; o servidor escolhe o
// repetidor da mesma sub-rede e manda a ele o job assinado; o repetidor conta que mandou; o
// check-in da máquina depois do pacote é a resposta.
func TestLigarPelaRede(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	agora := time.Now().UTC()
	abreSessao(t, st, sessaoTeste, agora)
	if err := st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", agora.Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := jobs.GenerateKey()
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithJobKey(priv))
	alvo, rep, longe := registra(t, h, tok), registra(t, h, tok), registra(t, h, tok)

	checkin := func(cred string, s *protocol.MachineState) protocol.CheckinResponse {
		t.Helper()
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), State: s,
			Capabilities: []string{protocol.CapabilityJobs}})
		r := envia(h, "POST", "/api/v1/agent/checkin", cred, corpo)
		if r.Code != http.StatusOK {
			t.Fatalf("check-in: %d %s", r.Code, r.Body)
		}
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}
	rede := func(relay bool, mac, ip string) *protocol.MachineState {
		return &protocol.MachineState{CollectedAt: agora, WakeRelay: relay,
			Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: mac, IPv4: []string{ip}, Up: true}}}
	}
	checkin(alvo.Credential, rede(true, "00:11:22:33:44:55", "192.0.2.20/24"))
	checkin(rep.Credential, rede(true, "00:aa:00:00:00:01", "192.0.2.30/24"))
	checkin(longe.Credential, rede(true, "00:aa:00:00:00:02", "198.51.100.4/24"))

	liga := func(cookie, maq string) *http.Response {
		t.Helper()
		return pagina(h, "POST", "/painel/maquinas/"+maq+"/ligar", cookie, nil).Result()
	}
	if r := liga(sessaoTeste, alvo.MachineID); r.StatusCode != http.StatusForbidden {
		t.Fatalf("leitura pede ligar: %d", r.StatusCode)
	}
	r := liga(sessaoAdmin, alvo.MachineID)
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "ligar=pedido&n=1") || !strings.Contains(loc, "aba=hardware") {
		t.Fatalf("pedido: %d %s", r.StatusCode, loc)
	}
	if loc := liga(sessaoAdmin, alvo.MachineID).Header.Get("Location"); !strings.Contains(loc, "ligar-erro=andamento") {
		t.Fatalf("segundo pedido: %s", loc)
	}
	// A máquina de outra sub-rede: ninguém para repetir.
	if loc := liga(sessaoAdmin, longe.MachineID).Header.Get("Location"); !strings.Contains(loc, "ligar-erro=repetidor") || !strings.Contains(loc, "198.51.100.0") {
		t.Fatalf("sem repetidor: %s", loc)
	}

	pedidos, _ := st.WakeRequests(ctx, alvo.MachineID, 5)
	if len(pedidos) != 1 || pedidos[0].Network != "192.0.2.0/24" || len(pedidos[0].Relays) != 1 || pedidos[0].Relays[0] != rep.MachineID {
		t.Fatalf("pedido gravado: %+v", pedidos)
	}
	// O job vai para o repetidor (e só para ele), assinado, com o MAC da máquina.
	if resp := checkin(longe.Credential, nil); len(resp.Jobs) != 0 {
		t.Fatal("a máquina de outra sub-rede recebeu o job")
	}
	resp := checkin(rep.Credential, nil)
	if len(resp.Jobs) != 1 {
		t.Fatalf("repetidor recebeu %d jobs", len(resp.Jobs))
	}
	j, err := jobs.Verify(pub, resp.Jobs[0], rep.MachineID, time.Now(), 0)
	if err != nil || j.Type != jobs.TypeWakeOnLAN {
		t.Fatalf("job: %+v %v", j, err)
	}
	p, _ := jobs.ParseWakeOnLANParams(j.Params)
	if p.Target != alvo.MachineID || len(p.MACs) != 1 || p.MACs[0] != "00:11:22:33:44:55" || p.Network != "192.0.2.0/24" {
		t.Fatalf("parâmetros: %+v", p)
	}
	// O check-in da máquina ANTES do pacote não conta.
	checkin(alvo.Credential, nil)
	if pedidos, _ = st.WakeRequests(ctx, alvo.MachineID, 1); pedidos[0].AnsweredAt != nil {
		t.Fatal("respondeu antes do pacote")
	}
	corpo, _ := json.Marshal(protocol.JobResult{Status: protocol.JobResultOK, Detail: "pacote mágico para x", FinishedAt: time.Now().UTC()})
	if r := envia(h, "POST", protocol.JobResultPath+strconv.FormatInt(j.ID, 10)+"/result", rep.Credential, corpo); r.Code != http.StatusOK {
		t.Fatalf("resultado: %d %s", r.Code, r.Body)
	}
	if row, _, _ := st.JobForMachine(ctx, rep.MachineID, j.ID); row.State != jobs.StateDone || !strings.Contains(row.Detail, "pacote enviado") {
		t.Fatalf("job do repetidor: %+v", row)
	}
	pedidos, _ = st.WakeRequests(ctx, alvo.MachineID, 1)
	if pedidos[0].SentAt == nil || pedidos[0].SentBy != rep.MachineID || pedidos[0].AnsweredAt != nil {
		t.Fatalf("pacote: %+v", pedidos[0])
	}
	checkin(alvo.Credential, nil)
	if pedidos, _ = st.WakeRequests(ctx, alvo.MachineID, 1); pedidos[0].AnsweredAt == nil {
		t.Fatal("a máquina respondeu e o pedido não anotou")
	}
	// Respondido: pode pedir de novo.
	if loc := liga(sessaoAdmin, alvo.MachineID).Header.Get("Location"); !strings.Contains(loc, "ligar=pedido") {
		t.Fatalf("novo pedido depois da resposta: %s", loc)
	}
}

// O cartão "Ligar agora" na aba Hardware: botão para o admin, histórico com a resposta.
func TestCartaoLigarAgora(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	const id = "60ef97e4-0001"
	if err := st.SaveHostState(ctx, id, &protocol.MachineState{Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: "00:11:22:33:44:55", IPv4: []string{"192.0.2.20/24"}, Up: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	b := pagina(h, "GET", "/painel/maquinas/"+id+"?aba=hardware", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/maquinas/`+id+`/ligar"`) || !strings.Contains(b, "00:11:22:33:44:55") {
		t.Fatalf("sem o botão:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/maquinas/"+id+"?aba=hardware", sessaoTeste, nil).Body.String(); strings.Contains(b, "/ligar\"") {
		t.Fatal("leitura vê o botão")
	}
	sent := time.Now().Add(-20 * time.Minute)
	ans := sent.Add(3 * time.Minute)
	if _, err := st.CreateWakeRequest(ctx, store.WakeRequest{MachineID: id, RequestedAt: sent.Add(-time.Minute), RequestedBy: "thyago", Network: "192.0.2.0/24",
		MACs: []string{"00:11:22:33:44:55"}, Relays: []string{"7aa604f7-0002"}, JobIDs: []int64{7}}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.MarkWakeSent(ctx, 7, "7aa604f7-0002", sent)
	_ = st.MarkWakeAnswered(ctx, id, ans)
	b = pagina(h, "GET", "/painel/maquinas/"+id+"?aba=hardware&ligar=pedido&n=2", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"respondeu 3 min depois do pacote", "192.0.2.0/24", "Pedido de ligar criado para 2 repetidores", "vm"} {
		if !strings.Contains(b, quer) {
			t.Errorf("o cartão não tem %q", quer)
		}
	}
}
