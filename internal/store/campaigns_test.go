package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoCampanhas interface {
	contratoJobs
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error)
	Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error)
	RunningCampaigns(ctx context.Context) ([]campaign.Campaign, error)
	SaveCampaign(ctx context.Context, c campaign.Campaign, from string) (bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error)
}

func testarCampanhas(t *testing.T, st contratoCampanhas) {
	ctx := context.Background()
	if _, err := st.SaveInventory(ctx, "camp-a", protocol.InventoryReport{SchemaVersion: 1, Hash: "camp-a"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateCampaign(ctx, campaign.Campaign{Name: "bluez", Type: "update", Sources: []string{"bluez", "curl"}, Rings: []int{0, 2},
		UseWindow: true, Observe: 90 * time.Minute, TTL: 6 * time.Hour, CreatedBy: "thyago"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok, err := st.Campaign(ctx, id)
	if err != nil || !ok || c.Name != "bluez" || len(c.Sources) != 2 || len(c.Rings) != 2 || c.Rings[1] != 2 || !c.UseWindow ||
		c.Observe != 90*time.Minute || c.TTL != 6*time.Hour || c.State != campaign.StateRunning || c.LaunchedIdx != -1 || c.AcceptedIdx != -1 {
		t.Fatalf("lida: %+v %t %v", c, ok, err)
	}
	// Andamento.
	agora := time.Now().UTC().Truncate(time.Microsecond)
	c.CurrentIdx, c.LaunchedIdx, c.RingDoneAt, c.Detail = 1, 1, &agora, "observando"
	if ok, err := st.SaveCampaign(ctx, c, campaign.StateRunning); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if c2, _, _ := st.Campaign(ctx, id); c2.CurrentIdx != 1 || c2.RingDoneAt == nil || !c2.RingDoneAt.Equal(agora) || c2.Detail != "observando" {
		t.Errorf("gravada: %+v", c2)
	}
	if l, _ := st.RunningCampaigns(ctx); len(l) != 1 {
		t.Errorf("em andamento: %d", len(l))
	}
	c.State = campaign.StatePaused
	_, _ = st.SaveCampaign(ctx, c, campaign.StateRunning)
	// O laço, que leu "em andamento" antes da pausa, não desfaz a pausa do admin.
	c.State = campaign.StateRunning
	if ok, _ := st.SaveCampaign(ctx, c, campaign.StateRunning); ok {
		t.Error("gravou por cima da pausa")
	}
	if l, _ := st.RunningCampaigns(ctx); len(l) != 0 {
		t.Error("pausada não está em andamento")
	}
	if l, _ := st.Campaigns(ctx, 10); len(l) != 1 || l[0].State != campaign.StatePaused {
		t.Errorf("lista: %+v", l)
	}

	// Jobs da campanha, por anel; job avulso fica fora.
	for _, idx := range []int{1, 1, -1} {
		jid, _ := st.NextJobID(ctx)
		j := NewJob{ID: jid, MachineID: "camp-a", Type: "rescan", Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "t", NotAfter: agora.Add(time.Hour)}
		if idx >= 0 {
			j.CampaignID, j.CampaignIdx = id, idx
		}
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if l, err := st.CampaignJobs(ctx, id, 1); err != nil || len(l) != 2 || l[0].State != "pendente" || l[0].MachineID != "camp-a" {
		t.Errorf("jobs do anel: %+v %v", l, err)
	}
	if l, _ := st.CampaignJobs(ctx, id, 0); len(l) != 0 {
		t.Error("anel sem jobs")
	}
}

func TestCampanhasMemoria(t *testing.T) { testarCampanhas(t, NewMemory()) }

func TestCampanhasPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarCampanhas(t, NewPostgres(pool))
}
