package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// servidor sobe a API de verdade (memória), conta as requisições por caminho e pode
// "cair" (503) sob comando.
type servidor struct {
	*httptest.Server
	st      *store.Memory
	fora    atomic.Bool
	mu      sync.Mutex
	pedidos map[string]int
}

func (s *servidor) conta(p string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pedidos[p]
}

func novoServidor(t *testing.T, opts ...api.Option) (*servidor, string, string) {
	t.Helper()
	s := &servidor{st: store.NewMemory(), pedidos: map[string]int{}}
	h := api.New(s.st, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.fora.Load() {
			http.Error(w, "fora", http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		s.pedidos[r.URL.Path]++
		s.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)

	ctx := context.Background()
	_, th := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := s.st.CreateEnrollmentToken(ctx, th, "teste", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	cred, ch := identity.NewSecret(identity.CredentialPrefix)
	id := identity.NewMachineID()
	if _, err := s.st.Enroll(ctx, th, identity.NewMachine{ID: id, CredentialHash: ch}); err != nil {
		t.Fatal(err)
	}
	return s, id, cred
}

func inventarioDeTeste() protocol.InventoryReport {
	return protocol.InventoryReport{
		SchemaVersion: protocol.InventorySchemaVersion,
		CollectedAt:   time.Now().UTC(),
		Hash:          "sha256:" + strings.Repeat("a", 64),
		OS:            protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Hostname: "FJ106260"},
		Software:      []protocol.Software{{Name: "7-Zip", Version: "24.09"}},
	}
}

func agenteDeTeste(t *testing.T, srv *servidor, cred string, buscas *atomic.Int32, log io.Writer) *Agent {
	t.Helper()
	return &Agent{
		Client: NewClient(srv.URL, cred, "v-teste"),
		Inventory: func(context.Context) (protocol.InventoryReport, error) {
			return inventarioDeTeste(), nil
		},
		Scan: func(context.Context) protocol.PatchScanReport {
			buscas.Add(1)
			return protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
				Scans: []protocol.ScanResult{{Source: "wua-default"}}}
		},
		DataDir:   t.TempDir(),
		Interval:  time.Hour,
		ScanEvery: 24 * time.Hour,
		Logger:    slog.New(slog.NewTextHandler(log, nil)),
	}
}

func TestCicloCompletoDepoisSoCheckin(t *testing.T) {
	srv, id, cred := novoServidor(t)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	ctx := context.Background()

	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("primeiro ciclo: %v", out)
	}
	if srv.conta("/api/v1/agent/inventory") != 1 || srv.conta("/api/v1/agent/scan") != 1 || buscas.Load() != 1 {
		t.Fatalf("primeiro ciclo envia inventário e busca: inv=%d scan=%d buscas=%d",
			srv.conta("/api/v1/agent/inventory"), srv.conta("/api/v1/agent/scan"), buscas.Load())
	}
	if _, found, _ := srv.st.LatestInventory(ctx, id); !found {
		t.Error("o servidor deveria ter o inventário")
	}
	st, _ := loadState(a.DataDir)
	if d := st.NextScanAt.Sub(st.LastScanAt); d < 22*time.Hour || d > 26*time.Hour {
		t.Errorf("próxima busca em 24 h ± 2 h: %v", d)
	}
	if p, _ := loadPendingScan(a.DataDir); p != nil {
		t.Error("busca aceita não fica pendente")
	}

	// Segundo ciclo, logo depois: inventário igual não é reenviado, e a busca não está devida.
	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("segundo ciclo: %v", out)
	}
	if srv.conta("/api/v1/agent/checkin") != 2 || srv.conta("/api/v1/agent/inventory") != 1 || buscas.Load() != 1 {
		t.Errorf("segundo ciclo só faz o check-in: checkin=%d inv=%d buscas=%d",
			srv.conta("/api/v1/agent/checkin"), srv.conta("/api/v1/agent/inventory"), buscas.Load())
	}
}

func TestBuscaSobreviveAoServidorFora(t *testing.T) {
	srv, id, cred := novoServidor(t)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	ctx := context.Background()

	// O servidor cai depois do inventário e antes da busca.
	a.Scan = func(context.Context) protocol.PatchScanReport {
		buscas.Add(1)
		srv.fora.Store(true)
		return protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC()}
	}
	if out := a.CheckIn(ctx); out != outcomeTransient {
		t.Fatalf("servidor fora no envio da busca: esperado transitório, veio %v", out)
	}
	if p, _ := loadPendingScan(a.DataDir); p == nil {
		t.Fatal("a busca deveria ficar pendente no disco")
	}

	// O agente "reinicia" (estado só no disco) e o servidor volta.
	srv.fora.Store(false)
	b := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	b.DataDir = a.DataDir
	if out := b.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("depois da volta: %v", out)
	}
	if _, found, _ := srv.st.LatestScan(ctx, id); !found || buscas.Load() != 1 {
		t.Errorf("a busca pendente é enviada, sem refazer a busca: found=%t buscas=%d", found, buscas.Load())
	}
	if p, _ := loadPendingScan(a.DataDir); p != nil {
		t.Error("a pendência some depois do envio")
	}
}

func TestCredencialRecusadaNaoFazBusca(t *testing.T) {
	srv, _, _ := novoServidor(t)
	var buscas atomic.Int32
	var log bytes.Buffer
	a := agenteDeTeste(t, srv, "patchd_mac_revogada", &buscas, &log)
	if out := a.CheckIn(context.Background()); out != outcomeUnauthorized {
		t.Fatalf("credencial recusada: %v", out)
	}
	if buscas.Load() != 0 {
		t.Error("sem credencial válida, a busca (minutos de WUA) não deve rodar")
	}
	if !strings.Contains(log.String(), "patchd-agent enroll") || strings.Contains(log.String(), "patchd_mac_revogada") {
		t.Errorf("o log orienta o operador e nunca mostra a credencial:\n%s", log.String())
	}
}

func TestRunEsperaCrescenteEVolta(t *testing.T) {
	srv, _, cred := novoServidor(t)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	a.rand = maior
	srv.fora.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	var esperas []time.Duration
	a.sleep = func(_ context.Context, d time.Duration) error {
		esperas = append(esperas, d)
		if len(esperas) == 4 {
			srv.fora.Store(false) // o servidor volta antes do 4º ciclo
		}
		if len(esperas) == 6 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	quer := []time.Duration{
		5 * time.Minute,  // espera inicial
		30 * time.Second, // 1ª falha
		time.Minute,      // 2ª
		2 * time.Minute,  // 3ª
		66 * time.Minute, // sucesso: volta ao intervalo (+10%)
		66 * time.Minute,
	}
	if len(esperas) != len(quer) {
		t.Fatalf("esperas: %v", esperas)
	}
	for i := range quer {
		if esperas[i] != quer[i] {
			t.Errorf("espera %d = %v, esperado %v (todas: %v)", i, esperas[i], quer[i], esperas)
		}
	}
}
