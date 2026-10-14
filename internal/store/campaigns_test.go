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
	DismissJob(ctx context.Context, id int64, why string) (bool, error)
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
	if c.App != "" || c.Version != "" || c.Notes == nil || len(c.Notes) != 0 {
		t.Errorf("update sem programa e sem notas: %+v", c)
	}
	// Campanha de terceiros (Aula 14.7): o programa e a versão.
	tp, err := st.CreateCampaign(ctx, campaign.Campaign{Name: "libreoffice", Type: "thirdparty", App: "libreoffice", Version: "26.8.1.1",
		Rings: []int{0}, Observe: time.Hour, TTL: 24 * time.Hour, CreatedBy: "thyago"})
	if err != nil {
		t.Fatal(err)
	}
	if c3, _, _ := st.Campaign(ctx, tp); c3.App != "libreoffice" || c3.Version != "26.8.1.1" || len(c3.Sources) != 0 {
		t.Errorf("terceiros: %+v", c3)
	}
	c3, _, _ := st.Campaign(ctx, tp)
	c3.State = campaign.StateCanceled
	_, _ = st.SaveCampaign(ctx, c3, campaign.StateRunning)

	// Andamento, com a nota do anel liberado.
	agora := time.Now().UTC().Truncate(time.Microsecond)
	c.CurrentIdx, c.LaunchedIdx, c.RingDoneAt, c.Detail = 1, 1, &agora, "observando"
	c.Notes = []string{"anel 0: 1 job(s)", "anel 2: 0 job(s); fora: 1 sem pendência aprovada"}
	if ok, err := st.SaveCampaign(ctx, c, campaign.StateRunning); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if c2, _, _ := st.Campaign(ctx, id); c2.CurrentIdx != 1 || c2.RingDoneAt == nil || !c2.RingDoneAt.Equal(agora) || c2.Detail != "observando" ||
		len(c2.Notes) != 2 || c2.Notes[1] != "anel 2: 0 job(s); fora: 1 sem pendência aprovada" {
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
	if l, _ := st.Campaigns(ctx, 10); len(l) != 2 || l[1].State != campaign.StatePaused || l[0].State != campaign.StateCanceled {
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
	// Aula 14.9: o adiado (aceito pela restrição da 0030) traz quando encerrou e pode ser
	// dispensado; só o adiado.
	l, _ := st.CampaignJobs(ctx, id, 1)
	if ok, err := st.FinishJob(ctx, l[0].ID, "adiado", "adiado pelo agente: programa aberto", agora); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := st.DismissJob(ctx, l[1].ID, "x"); ok {
		t.Error("dispensou um pendente")
	}
	l, _ = st.CampaignJobs(ctx, id, 1)
	if l[0].State != "adiado" || l[0].FinishedAt == nil || !l[0].FinishedAt.Equal(agora) {
		t.Errorf("adiado: %+v", l[0])
	}
	if ok, err := st.DismissJob(ctx, l[0].ID, "nova tentativa dispensada: em dia"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	l, _ = st.CampaignJobs(ctx, id, 1)
	if l[0].State != "dispensado" || l[0].Detail != "adiado pelo agente: programa aberto; nova tentativa dispensada: em dia" {
		t.Errorf("dispensado: %+v", l[0])
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
