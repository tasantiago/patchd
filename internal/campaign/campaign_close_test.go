package campaign_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/window"
)

// planejadorComProcessos: o plano de terceiros com a lista de processos da regra.
type planejadorComProcessos struct{ planejadorFalso }

func (p *planejadorComProcessos) Job(ctx context.Context, id, app, version string) (*jobs.ThirdPartyParams, string, error) {
	j, skip, err := p.planejadorFalso.Job(ctx, id, app, version)
	if j != nil {
		j.Processes = []string{"firefox.exe"}
	}
	return j, skip, err
}

// Programa aberto: com a opção, o job leva o "fechar com aviso" (só a agente que anuncia a
// capacidade); o adiado cujo programa fechou ganha a nova tentativa na hora, sem esperar o
// intervalo. A opção só vale em terceiros e com horário.
func TestCampanhaProgramaAberto(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	a.e.ThirdParty = &planejadorComProcessos{planejadorFalso{decisao: map[string]string{"b1": "sem o programa"}}}
	sabe := []string{"p1", "a1"}
	a.e.CanCloseApps = func(_ context.Context, id string) (bool, error) { return slices.Contains(sabe, id), nil }
	madrugada := &window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: 3 * time.Hour, Location: time.UTC}
	base := campaign.Campaign{Name: "firefox", Type: jobs.TypeThirdParty, App: "firefox", Version: "145.0", Rings: []int{0, 1},
		TTL: 6 * time.Hour, CreatedBy: "t", CloseDelay: 15 * time.Minute}
	for _, c := range []campaign.Campaign{
		func() campaign.Campaign { c := base; return c }(), // com prazo: fecharia em pleno expediente
		func() campaign.Campaign {
			c := base
			c.Daily, c.Type, c.App, c.Version = madrugada, jobs.TypeRescan, "", ""
			return c
		}(),
		func() campaign.Campaign { c := base; c.Daily, c.CloseDelay = madrugada, 90*time.Minute; return c }(),
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("deveria recusar: %+v", c)
		}
	}
	c := base
	c.Daily = madrugada
	id := a.cria(c)

	params := func(jobID int64, m string) jobs.ThirdPartyParams {
		t.Helper()
		row, ok, _ := a.st.JobForMachine(ctx, m, jobID)
		var j jobs.Job
		if !ok || json.Unmarshal([]byte(row.Payload), &j) != nil {
			t.Fatalf("job %d de %s", jobID, m)
		}
		p, err := jobs.ParseThirdPartyParams(j.Params)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a.passo(id, 0)
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 1 {
		t.Fatalf("grupo 0: %+v", l)
	}
	if p := params(l[0].ID, "p1"); p.Close == nil || p.Close.NoticeMinutes != 15 {
		t.Fatalf("p1 sabe fechar: %+v", p.Close)
	}

	// Adiado; o programa fecha 5 minutos depois: a nova tentativa sai no passo seguinte.
	a.encerra(id, 0, jobs.StatePostponed)
	if n, _ := a.st.MarkJobsUnblocked(ctx, "p1", []int64{l[0].ID}, a.agora.Add(5*time.Minute)); n != 1 {
		t.Fatal("liberar")
	}
	if n, _ := a.st.MarkJobsUnblocked(ctx, "a1", []int64{l[0].ID}, a.agora); n != 0 {
		t.Error("outra máquina liberou o job")
	}
	a.passo(id, 4*time.Minute)
	if l, _ = a.st.CampaignJobs(ctx, id, 0); len(l) != 1 {
		t.Fatal("antes de fechar, sem job novo")
	}
	c = a.passo(id, 6*time.Minute)
	if l, _ = a.st.CampaignJobs(ctx, id, 0); len(l) != 2 || l[1].State != jobs.StatePending {
		t.Fatalf("tentar assim que fechar: %+v %s", l, c.Detail)
	}

	// Grupo 1: a1 anuncia a capacidade, a2 não (o job vai sem fechar, como hoje).
	if ok, _ := a.st.FinishJob(ctx, l[1].ID, jobs.StateDone, "instalado", a.agora.Add(7*time.Minute)); !ok {
		t.Fatal("encerrar")
	}
	a.passo(id, 30*time.Hour)
	l, _ = a.st.CampaignJobs(ctx, id, 1)
	got := map[string]bool{}
	for _, j := range l {
		got[j.MachineID] = params(j.ID, j.MachineID).Close != nil
	}
	if len(got) != 2 || !got["a1"] || got["a2"] {
		t.Errorf("grupo 1: %v", got)
	}
}
