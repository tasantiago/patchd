package campaign_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
)

// Reenvio das falhas: os automáticos (o expirado vai de novo na hora; o que falhou, depois de
// 1 h; o recusado não volta) e o pedido do admin pelos motivos escolhidos.
func TestReenvioDasFalhas(t *testing.T) {
	ctx := context.Background()
	a := novoAmbiente(t)
	id := a.cria(campaign.Campaign{Name: "busca", Type: jobs.TypeRescan, Rings: []int{1}, Observe: time.Hour, TTL: time.Hour, CreatedBy: "t", AutoResend: 1})
	ultimos := func() map[string]campaign.JobState {
		l, _ := a.st.CampaignJobs(ctx, id, 0)
		out := map[string]campaign.JobState{}
		for _, j := range l {
			out[j.MachineID] = j
		}
		return out
	}
	total := func() int { l, _ := a.st.CampaignJobs(ctx, id, 0); return len(l) }
	fecha := func(maq, estado string, quando time.Duration) {
		t.Helper()
		if ok, _ := a.st.FinishJob(ctx, ultimos()[maq].ID, estado, "teste", a.agora.Add(quando)); !ok {
			t.Fatal("encerrar")
		}
	}
	a.passo(id, 0)
	fecha("a1", jobs.StateExpired, 0)
	fecha("a2", jobs.StateFailed, 0)
	c := a.passo(id, time.Minute)
	if c.State != campaign.StateRunning || total() != 3 || ultimos()["a1"].State != jobs.StatePending {
		t.Fatalf("o expirado vai de novo na hora: %d jobs, %+v", total(), c)
	}
	if !strings.Contains(c.Detail, "1 adiada(s), nova tentativa a partir de 2026-10-09 15:00 UTC") {
		t.Errorf("o que falhou espera 1 h: %s", c.Detail)
	}
	c = a.passo(id, time.Hour+time.Minute)
	if total() != 4 || ultimos()["a2"].State != jobs.StatePending {
		t.Fatalf("o que falhou, depois de 1 h: %d jobs, %+v", total(), c)
	}
	// Esgotado o reenvio automático (1: o job novo de a1 expirou de novo, pelo prazo), e o
	// recusado: a campanha pausa, com o resumo.
	if j := ultimos()["a1"]; j.State != jobs.StateExpired {
		t.Fatalf("a1: %+v", j)
	}
	fecha("a2", jobs.StateRejected, 2*time.Hour)
	c = a.passo(id, 2*time.Hour+time.Minute)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "2 job(s) sem sucesso (1 expirado, 1 rejeitado); retome") {
		t.Fatalf("pausa: %+v", c)
	}
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if by := campaign.FailedByReason(l, a.agora.Add(3*time.Hour), c.AutoResend); by[campaign.ReasonExpired] != 1 || by[campaign.ReasonRejected] != 1 {
		t.Errorf("por motivo: %v", by)
	}

	// O admin reenvia só os expirados: a campanha volta a andar e só a1 recebe job novo.
	if err := campaign.RequestResend(ctx, a.st, c, []string{"qualquer"}, "thyago"); err == nil {
		t.Error("motivo desconhecido aceito")
	}
	if err := campaign.RequestResend(ctx, a.st, c, []string{campaign.ReasonExpired}, "thyago"); err != nil {
		t.Fatal(err)
	}
	c, _, _ = a.st.Campaign(ctx, id)
	if c.State != campaign.StateRunning || !strings.Contains(c.Detail, "reenvio pedido por thyago (expirado)") {
		t.Fatalf("pedido: %+v", c)
	}
	c = a.passo(id, 3*time.Hour)
	if total() != 5 || ultimos()["a1"].State != jobs.StatePending || ultimos()["a2"].State != jobs.StateRejected {
		t.Fatalf("reenvio: %d jobs %+v", total(), ultimos())
	}
	if n := c.Notes[len(c.Notes)-1]; !strings.Contains(n, "reenvio pedido (expirado): 1 job(s) novo(s), 0 dispensado(s)") {
		t.Errorf("nota: %q", n)
	}
	if c.State != campaign.StateRunning {
		t.Fatalf("depois do reenvio: %+v", c)
	}
	// O pedido é consumido: o minuto seguinte não reenvia de novo.
	a.passo(id, 3*time.Hour+time.Minute)
	if total() != 5 {
		t.Error("reenviou duas vezes")
	}
	// a1 conclui; a2 (recusado, não reenviado) pausa de novo.
	fecha("a1", jobs.StateDone, 4*time.Hour)
	if c = a.passo(id, 4*time.Hour+time.Minute); c.State != campaign.StatePaused || !strings.Contains(c.Detail, "1 job(s) sem sucesso (1 rejeitado)") {
		t.Fatalf("de novo: %+v", c)
	}
	// Campanha cancelada: sem reenvio.
	c.State = campaign.StateCanceled
	if err := campaign.RequestResend(ctx, a.st, c, []string{campaign.ReasonRejected}, "t"); !errors.Is(err, campaign.ErrState) {
		t.Errorf("cancelada: %v", err)
	}
	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, AutoResend: 6}).Validate(); err == nil {
		t.Error("reenvios demais aceitos")
	}
}
