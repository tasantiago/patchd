package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoPedidos interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SetSelfServiceOffer(ctx context.Context, o SelfServiceOffer) error
	SelfServiceOffers(ctx context.Context) ([]SelfServiceOffer, error)
	CreateSelfServiceRequest(ctx context.Context, q SelfServiceRequest) (int64, error)
	SelfServiceRequests(ctx context.Context, f RequestFilter) ([]SelfServiceRequest, error)
	SelfServiceRequestByID(ctx context.Context, id int64) (SelfServiceRequest, bool, error)
	DecideSelfServiceRequest(ctx context.Context, id int64, state, by, note string, jobID int64, at time.Time) error
	ExpireSelfServiceRequests(ctx context.Context, before, now time.Time) (int64, error)
}

// Aula 18.3: a oferta que exige aprovação e os pedidos que esperam a TI.
func testarPedidos(t *testing.T, st contratoPedidos) {
	ctx := context.Background()
	if err := st.SetSelfServiceOffer(ctx, SelfServiceOffer{AppID: "vscode", CanInstall: true, NeedsApproval: true, UpdatedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.SelfServiceOffers(ctx); len(l) != 1 || !l[0].NeedsApproval {
		t.Fatalf("oferta: %+v", l)
	}
	for _, id := range []string{"maq-a", "maq-b"} {
		_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id})
	}
	pede := func(maq, conta string) int64 {
		t.Helper()
		id, err := st.CreateSelfServiceRequest(ctx, SelfServiceRequest{MachineID: maq, Account: conta, Groups: []string{`EXEMPLO\GG-Dev`},
			AppID: "vscode", Action: "instalar", Version: "1.105.1"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a1, a2, b1 := pede("maq-a", `EXEMPLO\ana`), pede("maq-a", `EXEMPLO\bia`), pede("maq-b", `EXEMPLO\ana`)
	if l, _ := st.SelfServiceRequests(ctx, RequestFilter{Waiting: true}); len(l) != 3 || l[0].ID != b1 || l[2].State != RequestWaiting {
		t.Errorf("aguardando: %+v", l)
	}
	if l, _ := st.SelfServiceRequests(ctx, RequestFilter{MachineID: "maq-a", Account: `exemplo\ANA`}); len(l) != 1 || l[0].ID != a1 ||
		l[0].Groups[0] != `EXEMPLO\GG-Dev` || l[0].Version != "1.105.1" {
		t.Errorf("da ana na maq-a: %+v", l)
	}
	agora := time.Now().UTC()
	if err := st.DecideSelfServiceRequest(ctx, a1, RequestApproved, "thyago", "", 77, agora); err != nil {
		t.Fatal(err)
	}
	if err := st.DecideSelfServiceRequest(ctx, a1, RequestRefused, "outro", "não", 0, agora); !errors.Is(err, ErrRequestDecided) {
		t.Errorf("segunda decisão: %v", err)
	}
	if err := st.DecideSelfServiceRequest(ctx, a2, RequestRefused, "thyago", "fora do escopo do setor", 0, agora); err != nil {
		t.Fatal(err)
	}
	q, ok, _ := st.SelfServiceRequestByID(ctx, a1)
	if !ok || q.State != RequestApproved || q.JobID != 77 || q.DecidedBy != "thyago" || q.DecidedAt == nil {
		t.Errorf("aprovado: %+v", q)
	}
	q, _, _ = st.SelfServiceRequestByID(ctx, a2)
	if q.State != RequestRefused || q.Note != "fora do escopo do setor" || q.JobID != 0 {
		t.Errorf("recusado: %+v", q)
	}
	if _, ok, _ := st.SelfServiceRequestByID(ctx, 999); ok {
		t.Error("pedido inexistente")
	}
	// Expira o que ainda aguarda (o da maq-b).
	if n, err := st.ExpireSelfServiceRequests(ctx, agora.Add(time.Hour), agora); err != nil || n != 1 {
		t.Errorf("expirar: %d %v", n, err)
	}
	if l, _ := st.SelfServiceRequests(ctx, RequestFilter{Waiting: true}); len(l) != 0 {
		t.Errorf("depois de expirar: %+v", l)
	}
}

func TestPedidosMemoria(t *testing.T) { testarPedidos(t, NewMemory()) }

func TestPedidosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarPedidos(t, NewPostgres(pool))
}
