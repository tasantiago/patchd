package standardization

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// contado conta as idas ao banco que pesam: o inventário inteiro, as políticas e a trava.
type contado struct {
	*store.Memory
	inventarios, politicas, travas int
}

func (c *contado) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	c.inventarios++
	return c.Memory.LatestInventory(ctx, id)
}

func (c *contado) StandardizationPolicies(ctx context.Context) ([]store.StandardizationPolicy, error) {
	c.politicas++
	return c.Memory.StandardizationPolicies(ctx)
}

func (c *contado) WithStandardizationLock(ctx context.Context, id string, fn func(context.Context) error) (bool, error) {
	c.travas++
	return c.Memory.WithStandardizationLock(ctx, id, fn)
}

// A avaliação de uma máquina só com atalhos não lê o inventário inteiro nem trava; as
// políticas vêm do cache até ele vencer ou ser renovado; com aplicativo, o inventário é lido
// uma vez só.
func TestAvaliacaoBarata(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	st := &contado{Memory: mem}
	if _, err := mem.SaveInventory(ctx, "pc", protocol.InventoryReport{SchemaVersion: 1, Hash: "h",
		OS: protocol.OSInfo{ID: "windows", Family: "windows", Arch: "amd64"}, Software: []protocol.Software{{Name: "x"}}}); err != nil {
		t.Fatal(err)
	}
	link := protocol.StandardShortcut{Name: "Portal", Kind: "site", Target: "https://example.com"}
	if _, err := mem.SaveStandardizationPolicy(ctx, store.StandardizationPolicy{Name: "Atalhos", Enabled: true, AllMachines: true,
		Shortcuts: []protocol.StandardShortcut{link}}); err != nil {
		t.Fatal(err)
	}
	agora := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	cache := &PolicyCache{TTL: 30 * time.Second}
	s := &Service{Store: st, Policies: cache, Now: func() time.Time { return agora }}
	for range 3 {
		plan, err := s.Reconcile(ctx, "pc", protocol.SelfServiceUser{Account: "ana"})
		if err != nil || len(plan.Shortcuts) != 1 {
			t.Fatalf("plano: %+v %v", plan, err)
		}
	}
	if st.inventarios != 0 || st.travas != 0 || st.politicas != 1 {
		t.Errorf("só atalhos: %d inventários, %d travas, %d leituras das políticas", st.inventarios, st.travas, st.politicas)
	}
	// O cache vence; e o Reset (o painel gravou uma política) lê de novo na hora.
	agora = agora.Add(31 * time.Second)
	s.Reconcile(ctx, "pc", protocol.SelfServiceUser{})
	cache.Reset()
	s.Reconcile(ctx, "pc", protocol.SelfServiceUser{})
	if st.politicas != 3 {
		t.Errorf("leituras das políticas depois de vencer e renovar: %d", st.politicas)
	}

	// Com aplicativo: o inventário inteiro uma vez, e a trava.
	if _, err := mem.SaveStandardizationPolicy(ctx, store.StandardizationPolicy{Name: "Apps", Enabled: true, AllMachines: true, Apps: []string{"7zip"}}); err != nil {
		t.Fatal(err)
	}
	cache.Reset()
	st.inventarios, st.travas = 0, 0
	if _, err := s.Reconcile(ctx, "pc", protocol.SelfServiceUser{}); err != nil {
		t.Fatal(err)
	}
	if st.inventarios != 1 || st.travas != 1 {
		t.Errorf("com aplicativo: %d inventários, %d travas", st.inventarios, st.travas)
	}

	// Inventário de agente antigo, sem o sistema à parte: lido inteiro para saber o sistema.
	if _, err := mem.SaveInventory(ctx, "velho", protocol.InventoryReport{SchemaVersion: 1, Hash: "v", OS: protocol.OSInfo{ID: "windows"}}); err != nil {
		t.Fatal(err)
	}
	st.inventarios = 0
	plan, err := s.Reconcile(ctx, "velho", protocol.SelfServiceUser{})
	if err != nil || st.inventarios != 1 || len(plan.Shortcuts) != 1 {
		t.Errorf("agente antigo: %+v %v, %d inventários", plan, err, st.inventarios)
	}
}
