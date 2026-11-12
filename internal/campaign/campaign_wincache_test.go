package campaign_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O cache do Windows na campanha (docs/cache-windows.md): a máquina que o parâmetro inclui
// recebe o job pelo cache, também com o KB só no catálogo offline (sem WSUS); a que não inclui,
// o job de sempre, e o KB só no catálogo offline fica de fora.
func TestCampanhaDoWindowsPeloCache(t *testing.T) {
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	soOffline := protocol.ScanResult{Source: "wua-offline", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}
	politica := protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{}}
	for _, c := range []struct {
		nome  string
		cache bool
		jobs  int
	}{{"pelo cache", true, 1}, {"sem o cache", false, 0}} {
		t.Run(c.nome, func(t *testing.T) {
			a := novoAmbiente(t)
			ctx := context.Background()
			a.e.Pending = func(context.Context, string) ([]protocol.PendingItem, bool, error) {
				return []protocol.PendingItem{{ID: "CVE-2026-1", FixedIn: "26300.9711 (KB5127648)"}}, true, nil
			}
			a.e.Scan = func(context.Context, string) (protocol.PatchScanReport, bool, error) {
				return protocol.PatchScanReport{Scans: []protocol.ScanResult{politica, soOffline}}, true, nil
			}
			a.e.WinCache = func(context.Context, string) (bool, error) { return c.cache, nil }
			a.passo(a.cria(campaign.Campaign{Name: "x", Type: jobs.TypeWinUpdate, Sources: []string{"KB5127648"}, Rings: []int{0},
				TTL: 6 * time.Hour, CreatedBy: "t"}), 0)
			env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
			if len(env) != c.jobs {
				t.Fatalf("%d job(s), queria %d", len(env), c.jobs)
			}
			if c.jobs == 0 {
				return
			}
			j, err := jobs.Verify(a.e.Key.Public().(ed25519.PublicKey), env[0], "p1", a.agora, 0)
			if err != nil {
				t.Fatal(err)
			}
			p, err := jobs.ParseWinUpdateParams(j.Params)
			if err != nil || !p.Cache || p.Fallback || p.Updates[0].ID != cu {
				t.Errorf("%+v %v", p, err)
			}
		})
	}
}
