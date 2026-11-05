package campaign_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
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
		a.t.Fatalf("grupo de implantação %d: %d job(s), esperado %d", idx, len(l), len(estados))
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

	// Grupo de implantação 0 liberado; os outros, não.
	c := a.passo(id, 0)
	if c.LaunchedIdx != 0 || !strings.Contains(c.Detail, "grupo de implantação 0: 1 de 1 job(s) em aberto") {
		t.Fatalf("lançamento: %+v", c)
	}
	if l, _ := a.st.CampaignJobs(context.Background(), id, 1); len(l) != 0 {
		t.Fatal("o grupo de implantação 1 não pode receber jobs antes do piloto")
	}
	// Piloto concluído: observação de 1 h.
	a.encerra(id, 0, jobs.StateDone)
	c = a.passo(id, time.Minute)
	if c.State != campaign.StateRunning || !strings.Contains(c.Detail, "observando até 2026-10-09 15:01 UTC antes do grupo de implantação 1") {
		t.Fatalf("observação: %+v", c)
	}
	if c = a.passo(id, 30*time.Minute); c.LaunchedIdx != 0 {
		t.Fatal("liberou antes do fim da observação")
	}
	// Fim da observação: o grupo de implantação 1 recebe os jobs, sozinho.
	c = a.passo(id, time.Hour+2*time.Minute)
	if c.CurrentIdx != 1 || c.LaunchedIdx != 1 || !strings.Contains(c.Detail, "grupo de implantação 1: 2 de 2") {
		t.Fatalf("liberação automática: %+v", c)
	}
	// Uma falha pausa; o grupo de implantação 2 espera o admin.
	a.encerra(id, 1, jobs.StateDone, jobs.StateFailed)
	c = a.passo(id, 2*time.Hour)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "1 job(s) sem sucesso") || !strings.Contains(c.Detail, "falhou") {
		t.Fatalf("pausa: %+v", c)
	}
	if c = a.passo(id, 5*time.Hour); c.State != campaign.StatePaused {
		t.Fatal("pausada não anda sozinha")
	}
	// Retomada: a falha fica aceita, a observação corre, e o grupo de implantação 2 vem depois.
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
	// No grupo de implantação 1, ninguém tem o bluez pendente: nenhum job, e a campanha termina.
	c := a.passo(id, 12*time.Minute)
	if c.State != campaign.StateDone {
		t.Fatalf("grupo de implantação sem pendência: %+v", c)
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

// Em horário fixo (ex.: 02:00 por 3 h, de madrugada), sem janela no grupo: o job espera o
// próximo horário e vale até o fim dele.
func TestCampanhaEmHorarioFixo(t *testing.T) {
	a := novoAmbiente(t)
	pvh, _ := time.LoadLocation("America/Porto_Velho")
	daily := &window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: 3 * time.Hour, Location: pvh}
	id := a.cria(campaign.Campaign{Name: "reinício de madrugada", Type: jobs.TypeRescan, Rings: []int{0}, Daily: daily, CreatedBy: "t"})
	if c := a.passo(id, 0); c.State != campaign.StateRunning {
		t.Fatalf("%+v", c)
	}
	l, _ := a.st.Jobs(context.Background(), "p1", 5)
	if len(l) != 1 || l[0].NotBefore == nil {
		t.Fatalf("jobs: %+v", l)
	}
	// a.agora é 09/10 14:00 UTC (10:00 em Porto Velho): o próximo 02:00 é 10/10 02:00 -04.
	quer := time.Date(2026, 10, 10, 2, 0, 0, 0, pvh)
	if !l[0].NotBefore.Equal(quer) || !l[0].NotAfter.Equal(quer.Add(3*time.Hour)) {
		t.Errorf("de %v até %v; quer %v por 3 h", l[0].NotBefore, l[0].NotAfter, quer)
	}
	// Horário fixo e janela do grupo juntos não: um ou outro.
	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, Daily: daily, UseWindow: true}).Validate(); err == nil {
		t.Error("janela e horário fixo juntos")
	}
	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, Daily: &window.Window{Days: window.AllDays, Start: time.Hour, Location: pvh}}).Validate(); err == nil {
		t.Error("horário fixo sem duração")
	}
}

func TestValidarCampanha(t *testing.T) {
	ok := campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0, 1}, TTL: time.Hour}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for nome, c := range map[string]campaign.Campaign{
		"sem nome":                            {Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour},
		"update sem fonte":                    {Name: "x", Type: jobs.TypeUpdate, Rings: []int{0}, TTL: time.Hour},
		"rescan com fonte":                    {Name: "x", Type: jobs.TypeRescan, Sources: []string{"a"}, Rings: []int{0}, TTL: time.Hour},
		"grupos de implantação fora de ordem": {Name: "x", Type: jobs.TypeRescan, Rings: []int{1, 0}, TTL: time.Hour},
		"grupo de implantação 10":             {Name: "x", Type: jobs.TypeRescan, Rings: []int{10}, TTL: time.Hour},
		"sem grupos de implantação":           {Name: "x", Type: jobs.TypeRescan, TTL: time.Hour},
		"prazo curto":                         {Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Minute},
		"terceiros sem versão":                {Name: "x", Type: jobs.TypeThirdParty, App: "vscode", Rings: []int{0}, TTL: time.Hour},
		"rescan com programa":                 {Name: "x", Type: jobs.TypeRescan, App: "vscode", Version: "1", Rings: []int{0}, TTL: time.Hour},
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

// planejadorFalso: a versão pronta ou não, e a decisão de cada máquina.
type planejadorFalso struct {
	motivo  string
	decisao map[string]string // máquina → motivo de ficar fora; ausente: recebe o job
	prontas int
}

func (p *planejadorFalso) Ready(context.Context, string, string) (string, error) {
	p.prontas++
	return p.motivo, nil
}

func (p *planejadorFalso) Job(_ context.Context, id, app, version string) (*jobs.ThirdPartyParams, string, error) {
	if skip, ok := p.decisao[id]; ok {
		return nil, skip, nil
	}
	return &jobs.ThirdPartyParams{App: app, Version: version, SHA256: strings.Repeat("a", 64), Size: 10, Installer: "msi",
		Args: []string{"/quiet"}, Signer: "The Document Foundation"}, "", nil
}

// Campanha de terceiros (Aula 14.7): a aprovação conferida a cada grupo de implantação, quem fica fora
// com o motivo, e as notas de cada grupo de implantação.
func TestCampanhaDeTerceiros(t *testing.T) {
	a := novoAmbiente(t)
	plan := &planejadorFalso{decisao: map[string]string{"a2": "em dia", "b1": "sem o programa"}}
	a.e.ThirdParty = plan
	id := a.cria(campaign.Campaign{Name: "libreoffice 26.8", Type: jobs.TypeThirdParty, App: "libreoffice", Version: "26.8.1.1",
		Rings: []int{0, 1, 2}, Observe: 10 * time.Minute, TTL: 24 * time.Hour, CreatedBy: "t"})
	c := a.passo(id, 0)
	l, _ := a.st.Jobs(context.Background(), "p1", 5)
	if len(l) != 1 || l[0].Type != jobs.TypeThirdParty || len(c.Notes) != 1 || c.Notes[0] != "grupo de implantação 0: 1 job(s)" || plan.prontas != 1 {
		t.Fatalf("piloto: %+v %+v", l, c)
	}
	env, _ := a.st.DeliverJobs(context.Background(), "p1", a.agora)
	var j jobs.Job
	_ = json.Unmarshal([]byte(env[0].Payload), &j)
	if p, err := jobs.ParseThirdPartyParams(j.Params); err != nil || p.App != "libreoffice" || p.Version != "26.8.1.1" {
		t.Errorf("parâmetros: %+v %v", p, err)
	}
	a.encerra(id, 0, jobs.StateDone)
	a.passo(id, time.Minute)

	// Aprovação revogada antes do grupo de implantação 1: a campanha pausa, sem job, com o motivo.
	plan.motivo = "libreoffice 26.8.1.1 não está aprovado (a aprovação pode ter sido revogada)"
	c = a.passo(id, 12*time.Minute)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "grupo de implantação 1 não liberado: libreoffice 26.8.1.1 não está aprovado") {
		t.Fatalf("revogada: %+v", c)
	}
	if l, _ := a.st.Jobs(context.Background(), "a1", 5); len(l) != 0 {
		t.Error("job no grupo de implantação 1 sem aprovação")
	}
	// Aprovada de novo e retomada: o grupo de implantação 1 sai; a2 fica fora (em dia).
	plan.motivo = ""
	if err := campaign.Resume(&c); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.st.SaveCampaign(context.Background(), c, campaign.StatePaused); !ok {
		t.Fatal("retomar")
	}
	c = a.passo(id, 13*time.Minute)
	if len(c.Notes) != 2 || c.Notes[1] != "grupo de implantação 1: 1 job(s); fora: 1 em dia" {
		t.Fatalf("grupo de implantação 1: %+v", c)
	}
	a.encerra(id, 1, jobs.StateDone)
	a.passo(id, 14*time.Minute)
	c = a.passo(id, 30*time.Minute)
	if c.State != campaign.StateDone || len(c.Notes) != 3 || c.Notes[2] != "grupo de implantação 2: 0 job(s); fora: 1 sem o programa" || c.Target() != "libreoffice 26.8.1.1" {
		t.Errorf("fim: %+v", c)
	}

	// Sem planejador (servidor sem as regras): pausa no primeiro grupo de implantação.
	a.e.ThirdParty = nil
	id2 := a.cria(campaign.Campaign{Name: "x", Type: jobs.TypeThirdParty, App: "vscode", Version: "1.141.0", Rings: []int{0}, TTL: time.Hour, CreatedBy: "t"})
	if c := a.passo(id2, 31*time.Minute); c.State != campaign.StatePaused || !strings.Contains(c.Detail, "sem as regras de terceiros") {
		t.Errorf("sem planejador: %+v", c)
	}
}

// Aula 14.9: o adiado ganha nova tentativa depois de RetryAfter; se o plano mudou, é
// dispensado; esgotadas as tentativas, pausa; revogada a versão, a nova tentativa espera.
func TestCampanhaComAdiados(t *testing.T) {
	a := novoAmbiente(t)
	plan := &planejadorFalso{decisao: map[string]string{"a2": "em dia", "b1": "sem o programa"}}
	a.e.ThirdParty = plan
	ctx := context.Background()
	id := a.cria(campaign.Campaign{Name: "vscode", Type: jobs.TypeThirdParty, App: "vscode", Version: "1.141.0",
		Rings: []int{0, 1}, TTL: 24 * time.Hour, CreatedBy: "t"})
	a.passo(id, 0)
	a.encerra(id, 0, jobs.StatePostponed) // programa aberto às 14:00

	c := a.passo(id, time.Minute)
	if c.State != campaign.StateRunning || !strings.Contains(c.Detail, "1 adiada(s), nova tentativa a partir de 2026-10-09 15:00 UTC") {
		t.Fatalf("esperando: %+v", c)
	}
	// Passada a hora: um job novo para a mesma máquina, no mesmo grupo de implantação.
	c = a.passo(id, 61*time.Minute)
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 2 || l[1].MachineID != "p1" || l[1].State != jobs.StatePending || !strings.Contains(c.Detail, "1 de 1 job(s) em aberto") {
		t.Fatalf("nova tentativa: %+v %+v", l, c)
	}
	// Adiado de novo, e a versão é revogada: a nova tentativa espera a aprovação.
	if ok, _ := a.st.FinishJob(ctx, l[1].ID, jobs.StatePostponed, "programa aberto", a.agora.Add(62*time.Minute)); !ok {
		t.Fatal("encerrar")
	}
	plan.motivo = "vscode 1.141.0 não está aprovado"
	c = a.passo(id, 123*time.Minute)
	if c.State != campaign.StatePaused || !strings.Contains(c.Detail, "grupo de implantação 0: nova tentativa suspensa: vscode 1.141.0 não está aprovado") {
		t.Fatalf("revogada: %+v", c)
	}
	// Aprovada de novo e retomada; enquanto isso a máquina foi atualizada à mão: o plano diz
	// "em dia", o adiado vira dispensado, e o grupo de implantação 1 sai.
	plan.motivo, plan.decisao["p1"] = "", "em dia"
	_ = campaign.Resume(&c)
	_, _ = a.st.SaveCampaign(ctx, c, campaign.StatePaused)
	c = a.passo(id, 124*time.Minute)
	l, _ = a.st.CampaignJobs(ctx, id, 0)
	if l[1].State != jobs.StateDismissed || !strings.Contains(l[1].Detail, "nova tentativa dispensada: em dia") {
		t.Errorf("dispensado: %+v", l[1])
	}
	if c.CurrentIdx != 1 || len(c.Notes) != 2 {
		t.Errorf("grupo de implantação 1: %+v", c)
	}

	// Tentativas esgotadas: conta como falha e pausa.
	b := novoAmbiente(t)
	b.e.ThirdParty = &planejadorFalso{}
	id2 := b.cria(campaign.Campaign{Name: "x", Type: jobs.TypeThirdParty, App: "vscode", Version: "1.141.0", Rings: []int{0}, TTL: 24 * time.Hour, CreatedBy: "t"})
	at := time.Duration(0)
	for i := 0; i < campaign.MaxAttempts; i++ {
		b.passo(id2, at)
		l, _ := b.st.CampaignJobs(ctx, id2, 0)
		if len(l) != i+1 {
			t.Fatalf("tentativa %d: %d job(s)", i+1, len(l))
		}
		b.st.FinishJob(ctx, l[i].ID, jobs.StatePostponed, "programa aberto", b.agora.Add(at))
		at += campaign.RetryAfter
	}
	if c := b.passo(id2, at); c.State != campaign.StatePaused || !strings.Contains(c.Detail, "adiado na tentativa 24 de 24") {
		t.Errorf("esgotadas: %+v", c)
	}
}

// Aula 16.4: a campanha do Windows leva KBs. Cada máquina recebe os que corrigem alguma CVE
// aprovada dela e que a fonte da política mostra; quem não pode fica fora, com o motivo.
func TestCampanhaDoWindows(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	fix := "26300.9711 (KB5127648)"
	pend := map[string][]protocol.PendingItem{
		"p1": {{ID: "CVE-2026-1", FixedIn: fix}},
		"a1": {{ID: "CVE-2026-1", FixedIn: fix}},
		"a2": {{ID: "CVE-2026-1", FixedIn: fix}},
		// b1 sem nenhuma CVE aprovada do KB
	}
	busca := func(def protocol.ScanResult) protocol.PatchScanReport {
		return protocol.PatchScanReport{Scans: []protocol.ScanResult{def,
			{Source: "wua-offline", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}}}
	}
	scans := map[string]protocol.PatchScanReport{
		"p1": busca(protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}),
		"a1": busca(protocol.ScanResult{Source: "wua-default", Error: "Search: HRESULT 0x80244007"}),
		"a2": busca(protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{}}),
	}
	a.e.Pending = func(_ context.Context, id string) ([]protocol.PendingItem, bool, error) { return pend[id], true, nil }
	a.e.Scan = func(_ context.Context, id string) (protocol.PatchScanReport, bool, error) {
		r, ok := scans[id]
		return r, ok, nil
	}
	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeWinUpdate, Sources: []string{"5127648"}, Rings: []int{0}, TTL: time.Hour}).Validate(); err == nil {
		t.Error("KB fora do formato normalizado deveria recusar")
	}
	id := a.cria(campaign.Campaign{Name: "outubro", Type: jobs.TypeWinUpdate, Sources: []string{"KB5127648"}, Rings: []int{0, 1, 2},
		TTL: 6 * time.Hour, CreatedBy: "thyago"})

	c := a.passo(id, 0)
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 1 || l[0].MachineID != "p1" || c.Notes[0] != "grupo de implantação 0: 1 job(s)" {
		t.Fatalf("grupo de implantação 0: %+v %v", l, c.Notes)
	}
	env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
	j, err := jobs.Verify(a.e.Key.Public().(ed25519.PublicKey), env[0], "p1", a.agora, 0)
	p, _ := jobs.ParseWinUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeWinUpdate || len(p.Updates) != 1 || p.Updates[0].ID != cu || p.Updates[0].KB != "5127648" {
		t.Fatalf("job: %+v %+v %v", j, p, err)
	}

	// Outra instalação em andamento: adiado → nova tentativa depois do intervalo.
	a.encerra(id, 0, jobs.StatePostponed)
	a.passo(id, time.Minute)
	if l, _ := a.st.CampaignJobs(ctx, id, 0); len(l) != 1 {
		t.Fatalf("antes do intervalo, sem job novo: %d", len(l))
	}
	a.passo(id, campaign.RetryAfter+2*time.Minute)
	l, _ = a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 2 {
		t.Fatalf("nova tentativa: %d job(s)", len(l))
	}
	if ok, _ := a.st.FinishJob(ctx, l[1].ID, jobs.StateDone, "instalada(s), aguardando reinício", a.agora); !ok {
		t.Fatal("encerrar")
	}

	// Grupos de implantação 1 e 2: ninguém pode receber; a campanha conclui com os motivos nas notas.
	c = a.passo(id, campaign.RetryAfter+30*time.Hour)
	if c.State != campaign.StateDone || len(c.Notes) != 3 ||
		c.Notes[1] != "grupo de implantação 1: 0 job(s); fora: 1 fonte da política falhando, 1 só no catálogo offline" ||
		c.Notes[2] != "grupo de implantação 2: 0 job(s); fora: 1 sem pendência aprovada" {
		t.Errorf("fim: %s %q", c.State, c.Notes)
	}
}

// Aula 16.5: a campanha de reinício só manda job a quem a busca mostra com reinício
// pendente; o dispensado (o agente viu que já não havia) não pausa o grupo de implantação.
func TestCampanhaDeReinicio(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	sim, nao := true, false
	scans := map[string]protocol.PatchScanReport{
		"p1": {Reboot: &protocol.RebootStatus{Pending: &sim}},
		"a1": {Reboot: &protocol.RebootStatus{Pending: &sim}},
		"a2": {Reboot: &protocol.RebootStatus{Pending: &nao}},
		"b1": {Reboot: &protocol.RebootStatus{}}, // não soube dizer
	}
	a.e.Scan = func(_ context.Context, id string) (protocol.PatchScanReport, bool, error) {
		r, ok := scans[id]
		return r, ok, nil
	}
	for _, d := range []time.Duration{0, time.Minute, 2 * time.Hour} {
		if err := (campaign.Campaign{Name: "r", Type: jobs.TypeReboot, Rings: []int{0}, TTL: time.Hour, RebootDelay: d}).Validate(); err == nil {
			t.Errorf("aviso %v deveria recusar", d)
		}
	}
	if err := (campaign.Campaign{Name: "r", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, RebootDelay: 15 * time.Minute}).Validate(); err == nil {
		t.Error("aviso só no reinício")
	}
	id := a.cria(campaign.Campaign{Name: "reinício", Type: jobs.TypeReboot, Rings: []int{0, 1, 2}, TTL: 6 * time.Hour,
		RebootDelay: 30 * time.Minute, CreatedBy: "thyago"})

	c := a.passo(id, 0)
	if c.Notes[0] != "grupo de implantação 0: 1 job(s)" || c.Target() != "onde houver reinício pendente, com aviso de 30 min" {
		t.Fatalf("grupo de implantação 0: %v %s", c.Notes, c.Target())
	}
	env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
	j, err := jobs.Verify(a.e.Key.Public().(ed25519.PublicKey), env[0], "p1", a.agora, 0)
	p, _ := jobs.ParseRebootParams(j.Params)
	if err != nil || j.Type != jobs.TypeReboot || p.Delay() != 30*time.Minute {
		t.Fatalf("job: %+v %v", j, err)
	}
	// O agente viu que já não havia reinício: dispensado; o grupo de implantação conclui e o próximo libera.
	a.encerra(id, 0, jobs.StateDismissed)
	c = a.passo(id, 25*time.Hour)
	if c.CurrentIdx != 1 || len(c.Notes) != 2 || c.Notes[1] != "grupo de implantação 1: 1 job(s); fora: 1 sem reinício pendente" {
		t.Fatalf("grupo de implantação 1: idx %d %q %s", c.CurrentIdx, c.Notes, c.Detail)
	}
	a.encerra(id, 1, jobs.StateDone)
	c = a.passo(id, 50*time.Hour)
	if c.State != campaign.StateDone || c.Notes[2] != "grupo de implantação 2: 0 job(s); fora: 1 reinício pendente desconhecido" {
		t.Errorf("fim: %s %q", c.State, c.Notes)
	}
}
