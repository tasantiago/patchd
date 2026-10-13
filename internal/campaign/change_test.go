package campaign_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
)

func TestOperacoesDoAdmin(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	id := a.cria(campaign.Campaign{Name: "busca", Type: jobs.TypeRescan, Rings: []int{0, 1}, Observe: time.Hour, TTL: 24 * time.Hour})
	c := a.passo(id, 0) // anel 0 liberado: 1 job pendente
	if c.State != campaign.StateRunning || c.LaunchedIdx != 0 {
		t.Fatalf("liberação: %+v", c)
	}

	// Retomar uma em andamento: fora de estado, nada gravado.
	if _, err := campaign.Change(ctx, a.st, c, campaign.OpResume, "fulano"); !errors.Is(err, campaign.ErrState) {
		t.Errorf("retomar em andamento: %v", err)
	}
	// Pausar; quem pausou aparece no detalhe.
	if _, err := campaign.Change(ctx, a.st, c, campaign.OpPause, "fulano"); err != nil {
		t.Fatal(err)
	}
	c, _, _ = a.st.Campaign(ctx, id)
	if c.State != campaign.StatePaused || c.Detail != "pausada por fulano" {
		t.Errorf("pausada: %+v", c)
	}
	// Uma cópia velha (lida antes da pausa) não grava por cima: ErrChanged.
	velha := c
	velha.State = campaign.StateRunning
	if _, err := campaign.Change(ctx, a.st, velha, campaign.OpPause, "beltrano"); !errors.Is(err, campaign.ErrChanged) {
		t.Errorf("cópia velha: %v", err)
	}
	// Retomar e cancelar: o job pendente do anel 0 é cancelado.
	if _, err := campaign.Change(ctx, a.st, c, campaign.OpResume, "fulano"); err != nil {
		t.Fatal(err)
	}
	c, _, _ = a.st.Campaign(ctx, id)
	n, err := campaign.Change(ctx, a.st, c, campaign.OpCancel, "fulano")
	if err != nil || n != 1 {
		t.Fatalf("cancelar: %d %v", n, err)
	}
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 1 || l[0].State != jobs.StateCanceled {
		t.Errorf("jobs: %+v", l)
	}
	c, _, _ = a.st.Campaign(ctx, id)
	if c.State != campaign.StateCanceled || c.Detail != "cancelada por fulano" {
		t.Errorf("cancelada: %+v", c)
	}
	if _, err := campaign.Change(ctx, a.st, c, campaign.OpCancel, "fulano"); !errors.Is(err, campaign.ErrState) {
		t.Errorf("cancelar de novo: %v", err)
	}
	// O motor não ressuscita uma cancelada.
	if c := a.passo(id, time.Hour); c.State != campaign.StateCanceled {
		t.Errorf("motor depois do cancelamento: %s", c.State)
	}
}
