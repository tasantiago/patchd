package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/window"
)

type contratoProgramaAberto interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error)
	CreateJob(ctx context.Context, j NewJob) error
	NextJobID(ctx context.Context) (int64, error)
	DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error)
	FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error)
	MarkJobsUnblocked(ctx context.Context, machineID string, ids []int64, at time.Time) (int, error)
	SetMachineCapabilities(ctx context.Context, id string, caps []string) error
	MachineCapabilities(ctx context.Context, id string) ([]string, error)
}

// Programa aberto: as capacidades do agente, o prazo do aviso da campanha e o job liberado
// (só o adiado da própria máquina, uma vez).
func testarProgramaAberto(t *testing.T, st contratoProgramaAberto) {
	ctx := context.Background()
	for _, m := range []string{"pa-1", "pa-2"} {
		if _, err := st.SaveInventory(ctx, m, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + m}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetMachineCapabilities(ctx, "pa-1", []string{"fechar-programa-v1", "jobs-v1"}); err != nil {
		t.Fatal(err)
	}
	if caps, _ := st.MachineCapabilities(ctx, "pa-1"); strings.Join(caps, ",") != "fechar-programa-v1,jobs-v1" {
		t.Errorf("capacidades: %v", caps)
	}
	if caps, _ := st.MachineCapabilities(ctx, "pa-2"); len(caps) != 0 {
		t.Errorf("sem check-in: %v", caps)
	}
	cid, err := st.CreateCampaign(ctx, campaign.Campaign{Name: "ff", Type: jobs.TypeThirdParty, App: "firefox", Version: "1", Rings: []int{0},
		TTL: time.Hour, CreatedBy: "t", CloseDelay: 15 * time.Minute,
		Daily: &window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: 3 * time.Hour, Location: time.UTC}})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, _ := st.Campaign(ctx, cid); c.CloseDelay != 15*time.Minute {
		t.Errorf("aviso: %v", c.CloseDelay)
	}
	id, _ := st.NextJobID(ctx)
	if err := st.CreateJob(ctx, NewJob{ID: id, MachineID: "pa-1", Type: jobs.TypeThirdParty, Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
		CreatedBy: "t", NotAfter: time.Now().Add(time.Hour), CampaignID: cid}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	if n, _ := st.MarkJobsUnblocked(ctx, "pa-1", []int64{id}, at); n != 0 {
		t.Error("liberou um job que não está adiado")
	}
	st.DeliverJobs(ctx, "pa-1", time.Now())
	st.FinishJob(ctx, id, jobs.StatePostponed, "programa aberto", time.Now())
	if n, _ := st.MarkJobsUnblocked(ctx, "pa-2", []int64{id}, at); n != 0 {
		t.Error("outra máquina liberou")
	}
	if n, _ := st.MarkJobsUnblocked(ctx, "pa-1", []int64{id}, at); n != 1 {
		t.Fatal("liberar")
	}
	if n, _ := st.MarkJobsUnblocked(ctx, "pa-1", []int64{id}, at.Add(time.Hour)); n != 0 {
		t.Error("liberou duas vezes")
	}
	l, _ := st.CampaignJobs(ctx, cid, 0)
	if len(l) != 1 || l[0].Unblocked == nil || !l[0].Unblocked.Equal(at) {
		t.Errorf("job da campanha: %+v", l)
	}
}

func TestProgramaAbertoMemoria(t *testing.T) { testarProgramaAberto(t, NewMemory()) }

func TestProgramaAbertoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarProgramaAberto(t, NewPostgres(pool))
}
