package campaign_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

type ambiente struct {
	t     *testing.T
	st    *store.Memory
	e     *campaign.Engine
	agora time.Time
}

func novoAmbiente(t *testing.T) *ambiente {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemory()
	for maq, anel := range map[string]int{"p1": 0, "a1": 1, "a2": 1, "b1": 2} {
		if _, err := st.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: maq}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.SetMachineRing(ctx, maq, anel); !ok {
			t.Fatal("anel")
		}
	}
	_, key, _ := jobs.GenerateKey()
	pend := map[string][]protocol.PendingItem{
		"p1": {{ID: "bluez", FixedIn: "5.85-4ubuntu0.3", Packages: []string{"bluez", "libbluetooth3"}}, {ID: "curl", FixedIn: "8.18", Packages: []string{"curl"}}},
		"a1": {{ID: "curl", FixedIn: "8.18", Packages: []string{"curl"}}},
	}
	e := &campaign.Engine{Store: st, Key: key,
		Pending: func(_ context.Context, id string) ([]protocol.PendingItem, bool, error) { return pend[id], true, nil },
		Window: func(context.Context, string, time.Time) (time.Time, time.Time, bool, error) {
			return time.Time{}, time.Time{}, false, nil
		}}
	return &ambiente{t: t, st: st, e: e, agora: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)}
}

func (a *ambiente) cria(c campaign.Campaign) int64 {
	a.t.Helper()
	if err := c.Validate(); err != nil {
		a.t.Fatal(err)
	}
	id, err := a.st.CreateCampaign(context.Background(), c)
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

// passo avança a campanha no instante "depois" de a.agora e devolve o estado gravado.
func (a *ambiente) passo(id int64, depois time.Duration) campaign.Campaign {
	a.t.Helper()
	if err := a.e.Step(context.Background(), a.agora.Add(depois)); err != nil {
		a.t.Fatal(err)
	}
	c, _, _ := a.st.Campaign(context.Background(), id)
	return c
}

func (a *ambiente) encerra(campanha int64, idx int, estados ...string) {
	a.t.Helper()
	l, _ := a.st.CampaignJobs(context.Background(), campanha, idx)
	if len(l) != len(estados) {
		a.t.Fatalf("anel %d: %d job(s), esperado %d", idx, len(l), len(estados))
	}
	for i, j := range l {
		if ok, _ := a.st.FinishJob(context.Background(), j.ID, estados[i], "teste", a.agora); !ok {
			a.t.Fatal("encerrar")
		}
	}
}

func TestCampanhaPilotoPrimeiro(t *testing.T) {
	a := novoAmbiente(t)
	id := a.cria(campaign.Campaign{Name: "busca geral", Type: jobs.TypeRescan, Rings: []int{0, 1, 2}, Observe: time.Hour, TTL: time.Hour, CreatedBy: "t"})

	// Anel 0 liberado; os outros, não.
	c := a.passo(id, 0)
	if c.LaunchedIdx != 0 || !strings.Contains(c.Detail, "anel 0: 1 de 1 job(s) em aberto") {
		t.Fatalf("lançamento: %+v", c)
	}
	if l, _ := a.st.CampaignJobs(context.Background(), id, 1); len(l) != 0 {
		t.Fatal("o anel 1 não pode receber jobs antes do piloto")
	}
	// Piloto concluído: observação de 1 h.
	a.encerra(id, 0, jobs.StateDone)
	c = a.passo(id, time.Minute)
	if c.State != campaign.StateRunning || !strings.Contains(c.Detail, "observando até 2026-10-09 15:01 UTC antes do anel 1") {
		t.Fatalf("observação: %+v", c)
	}
	if c = a.passo(id, 30*time.Minute); c.LaunchedIdx != 0 {
		t.Fatal("liberou antes do fim da observação")
	}
	// Fim da observação: o anel 1 recebe os jobs, sozinho.
	c = a.passo(id, time.Hour+2*time.Minute)
	if c.CurrentIdx != 1 || c.LaunchedIdx != 1 || !strings.Contains(c.Detail, "anel 1: 2 de 2") {
		t.Fatalf("liberação automática: %+v", c)
	}
	// Uma falha pausa; o anel 2 espera o admin.
	a.encerra(id, 1, jobs.StateDone, jobs.StateFailed)
	c = a.passo(id, 2*time.Hour)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "1 job(s) sem sucesso") || !strings.Contains(c.Detail, "falhou") {
		t.Fatalf("pausa: %+v", c)
	}
	if c = a.passo(id, 5*time.Hour); c.State != campaign.StatePaused {
		t.Fatal("pausada não anda sozinha")
	}
	// Retomada: a falha fica aceita, a observação corre, e o anel 2 vem depois.
	if err := campaign.Resume(&c); err != nil {
		t.Fatal(err)
	}
	_, _ = a.st.SaveCampaign(context.Background(), c, campaign.StatePaused)
	c = a.passo(id, 5*time.Hour)
	if c.State != campaign.StateRunning || !strings.Contains(c.Detail, "observando") {
		t.Fatalf("retomada: %+v", c)
	}
	c = a.passo(id, 6*time.Hour+time.Minute)
	a.encerra(id, 2, jobs.StateDone)
	c = a.passo(id, 6*time.Hour+2*time.Minute)
	if c.State != campaign.StateDone || !strings.Contains(c.Detail, "0 → 1 → 2") {
		t.Fatalf("fim: %+v", c)
	}
	if err := campaign.Resume(&c); err == nil {
		t.Error("concluída não se retoma")
	}
}

func TestCampanhaDeUpdate(t *testing.T) {
	a := novoAmbiente(t)
	id := a.cria(campaign.Campaign{Name: "bluez", Type: jobs.TypeUpdate, Sources: []string{"bluez"}, Rings: []int{0, 1}, Observe: 10 * time.Minute,
		TTL: time.Hour, CreatedBy: "t"})
	a.passo(id, 0)
	l, _ := a.st.Jobs(context.Background(), "p1", 5)
	if len(l) != 1 || l[0].Type != jobs.TypeUpdate || l[0].CreatedBy != "campanha 1" {
		t.Fatalf("job do piloto: %+v", l)
	}
	a.encerra(id, 0, jobs.StateDone)
	a.passo(id, time.Minute)
	// No anel 1, ninguém tem o bluez pendente: nenhum job, e a campanha termina.
	c := a.passo(id, 12*time.Minute)
	if c.State != campaign.StateDone {
		t.Fatalf("anel sem pendência: %+v", c)
	}
	for _, m := range []string{"a1", "a2"} {
		if l, _ := a.st.Jobs(context.Background(), m, 5); len(l) != 0 {
			t.Errorf("%s recebeu job sem ter a fonte pendente", m)
		}
	}
}

func TestCampanhaNaJanelaSemJanelaPausa(t *testing.T) {
	a := novoAmbiente(t)
	id := a.cria(campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, UseWindow: true, CreatedBy: "t"})
	if c := a.passo(id, 0); c.State != campaign.StatePaused || !strings.Contains(c.Detail, "sem janela") {
		t.Fatalf("%+v", c)
	}
}

func TestValidarCampanha(t *testing.T) {
	ok := campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0, 1}, TTL: time.Hour}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for nome, c := range map[string]campaign.Campaign{
		"sem nome":            {Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour},
		"update sem fonte":    {Name: "x", Type: jobs.TypeUpdate, Rings: []int{0}, TTL: time.Hour},
		"rescan com fonte":    {Name: "x", Type: jobs.TypeRescan, Sources: []string{"a"}, Rings: []int{0}, TTL: time.Hour},
		"anéis fora de ordem": {Name: "x", Type: jobs.TypeRescan, Rings: []int{1, 0}, TTL: time.Hour},
		"anel 10":             {Name: "x", Type: jobs.TypeRescan, Rings: []int{10}, TTL: time.Hour},
		"sem anéis":           {Name: "x", Type: jobs.TypeRescan, TTL: time.Hour},
		"prazo curto":         {Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Minute},
	} {
		if c.Validate() == nil {
			t.Errorf("%s: aceita", nome)
		}
	}
	if r, err := campaign.ParseRings("0, 1,3"); err != nil || len(r) != 3 || r[2] != 3 {
		t.Errorf("ParseRings: %v %v", r, err)
	}
	if _, err := campaign.ParseRings("1,0"); err == nil {
		t.Error("fora de ordem")
	}
}
