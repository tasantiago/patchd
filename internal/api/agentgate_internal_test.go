package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A fila dos agentes: vagas limitadas, espera com prazo, e a conta para a aba Saúde.
func TestFilaDosAgentes(t *testing.T) {
	for max, quer := range map[int]int{2: 0, 3: 0, 4: 2, 8: 5, 16: 11, 32: 23} {
		if got := AgentGateSlots(max); got != quer {
			t.Errorf("pool %d: %d vagas, queria %d", max, got, quer)
		}
	}
	var semFila *agentGate
	if release, ok := semFila.acquire(context.Background()); !ok {
		t.Fatal("sem fila, sempre passa")
	} else {
		release()
	}
	if _, ok := semFila.stats(); ok {
		t.Error("sem fila, sem números")
	}

	g := newAgentGate(1, 30*time.Millisecond)
	release, ok := g.acquire(context.Background())
	if !ok {
		t.Fatal("primeira vaga")
	}
	if _, ok := g.acquire(context.Background()); ok {
		t.Fatal("segunda vaga com uma só")
	}
	// Quem pediu foi embora: sai da fila na hora.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	longa := newAgentGate(1, time.Hour)
	longa.slots <- struct{}{}
	if _, ok := longa.acquire(ctx); ok {
		t.Error("contexto cancelado ganhou vaga")
	}
	// A vaga solta enquanto alguém espera vai para quem espera.
	espera := newAgentGate(1, 5*time.Second)
	solta, _ := espera.acquire(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); solta() }()
	if r2, ok := espera.acquire(context.Background()); !ok {
		t.Error("não pegou a vaga solta")
	} else {
		r2()
	}
	release()
	st, _ := g.stats()
	if st.Slots != 1 || st.InUse != 0 || st.Waited != 1 || st.Rejected != 1 || st.AvgWait < 20*time.Millisecond {
		t.Errorf("números: %+v", st)
	}
}

// Nas rotas: com a fila cheia, o check-in recebe 503 "busy" com Retry-After (o agente tenta
// depois); o canal em tempo real não entra na fila; o download solta a vaga antes de
// transferir; e a requisição atendida devolve a vaga.
func TestFilaDosAgentesNasRotas(t *testing.T) {
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	var gate *agentGate
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), func(a *api) {
		a.gate = newAgentGate(1, 20*time.Millisecond)
		gate = a.gate
	})
	envia := func(method, path, bearer string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	corpo, _ := json.Marshal(protocol.EnrollRequest{AgentVersion: "v0.4.0-teste",
		Evidence: protocol.IdentityEvidence{InstallID: "guid-1", Hostname: "pc-teste", OSFamily: "linux"}})
	var m protocol.EnrollResponse
	if r := envia("POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &m) != nil {
		t.Fatalf("enrollment: %d", r.Code)
	}
	checkin, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v0.4.0-teste"})

	if r := envia("POST", "/api/v1/agent/checkin", m.Credential, checkin); r.Code != http.StatusOK || len(gate.slots) != 0 {
		t.Fatalf("fila livre: %d, %d vaga(s) presa(s)", r.Code, len(gate.slots))
	}
	gate.slots <- struct{}{} // outra requisição segura a única vaga
	r := envia("POST", "/api/v1/agent/checkin", m.Credential, checkin)
	if r.Code != http.StatusServiceUnavailable || r.Header().Get("Retry-After") != "30" || !strings.Contains(r.Body.String(), `"busy"`) {
		t.Errorf("fila cheia: %d %q %s", r.Code, r.Header().Get("Retry-After"), r.Body)
	}
	if r := envia("POST", protocol.ChannelPath, m.Credential, nil); r.Code != http.StatusOK {
		t.Errorf("o canal entrou na fila: %d", r.Code)
	}
	// Sem credencial no formato: recusado antes da fila (não gasta a espera).
	if r := envia("POST", "/api/v1/agent/checkin", "qualquer", checkin); r.Code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", r.Code)
	}
	<-gate.slots
	// O download: a vaga cobre só a credencial; aqui o arquivo não existe (404), e a vaga voltou.
	if r := envia("GET", "/api/v1/agent/releases/v9.9.9/patchd-agent-linux-amd64", m.Credential, nil); r.Code == http.StatusServiceUnavailable || len(gate.slots) != 0 {
		t.Errorf("download: %d, %d vaga(s) presa(s)", r.Code, len(gate.slots))
	}
	if s, _ := gate.stats(); s.Rejected != 1 || s.Waited != 1 {
		t.Errorf("números: %+v", s)
	}
}

// A lista do Inventário guardada: dentro do prazo, uma leitura do banco só; um formulário do
// painel zera; desligada (o padrão dos testes), lê sempre.
func TestListaDoInventarioGuardada(t *testing.T) {
	agora := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	banco := &contaLista{Memory: store.NewMemory()}
	a := &api{store: banco, now: func() time.Time { return agora }}
	ctx := context.Background()
	for range 3 {
		if _, err := a.inventoryList(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if banco.n != 3 {
		t.Fatalf("desligada: %d leituras", banco.n)
	}
	banco.n, a.invList.ttl = 0, 15*time.Second
	for range 3 {
		_, _ = a.inventoryList(ctx)
	}
	agora = agora.Add(14 * time.Second)
	_, _ = a.inventoryList(ctx)
	if banco.n != 1 {
		t.Errorf("dentro do prazo: %d leituras", banco.n)
	}
	agora = agora.Add(2 * time.Second)
	_, _ = a.inventoryList(ctx)
	if banco.n != 2 {
		t.Errorf("prazo vencido: %d leituras", banco.n)
	}
	a.listChanged()
	_, _ = a.inventoryList(ctx)
	if banco.n != 3 {
		t.Errorf("depois de um formulário: %d leituras", banco.n)
	}
	agora = agora.Add(-time.Hour) // o relógio voltou: não confia na cópia
	_, _ = a.inventoryList(ctx)
	if banco.n != 4 {
		t.Errorf("relógio voltou: %d leituras", banco.n)
	}
}

type contaLista struct {
	*store.Memory
	n int
}

func (c *contaLista) InventoryList(ctx context.Context) ([]store.InventoryRow, error) {
	c.n++
	return c.Memory.InventoryList(ctx)
}
