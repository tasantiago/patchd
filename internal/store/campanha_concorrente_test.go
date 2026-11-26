package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Várias instâncias do servidor no mesmo banco avançando a mesma campanha por localização ao
// mesmo tempo (a trava consultiva do laço, como em cmd/server): cada alvo recebe um job só.
func TestCampanhaPorPredioComVariasInstancias(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	pg := NewPostgres(pool)
	var targets []campaign.Target
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
		if _, err := pg.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, CollectedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, campaign.Target{MachineID: id, Via: campaign.ViaUnlocated})
	}
	sc := &campaign.Scope{Unlocated: true, Stages: []campaign.Stage{{Label: "Equipamentos sem localização identificada", Machines: len(targets)}}}
	id, err := pg.CreateLocatedCampaign(ctx, campaign.Campaign{Name: "concorrente", Type: jobs.TypeRescan, Scope: sc, TTL: time.Hour, CreatedBy: "t"}, targets)
	if err != nil {
		t.Fatal(err)
	}
	_, key, _ := jobs.GenerateKey()
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eng := &campaign.Engine{Store: pg, Key: key}
			for range 3 {
				if _, err := pg.WithAdvisoryLock(ctx, LockCampaigns, func(ctx context.Context) error { return eng.Step(ctx, now) }); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	list, err := pg.CampaignJobs(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, j := range list {
		seen[j.MachineID]++
	}
	if len(list) != len(targets) || len(seen) != len(targets) {
		t.Fatalf("jobs: %d para %d alvos (%v)", len(list), len(targets), seen)
	}
}
