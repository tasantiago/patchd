package campaign_test

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Todas as atualizações do sistema (pedido de 04/11/2026): cada máquina recebe o job do sistema
// dela com tudo o que está aprovado e pendente; o macOS e quem não tem nada ficam fora.
func TestCampanhaTodasDoSistema(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	pend := map[string][]protocol.PendingItem{
		"p1": {{ID: "bluez", FixedIn: "5.85-4ubuntu0.3", Packages: []string{"bluez", "libbluetooth3"}}, {ID: "curl", FixedIn: "8.18", Packages: []string{"curl"}}},
		"a1": {{ID: "CVE-2026-1", FixedIn: "26300.9711 (KB5127648)"}, {ID: "CVE-2026-2", FixedIn: "26300.9711 (KB5127648)"}},
		"a2": {{ID: "CVE-2026-3", FixedIn: "26.1"}}, // macOS
	}
	a.e.Pending = func(_ context.Context, id string) ([]protocol.PendingItem, bool, error) { return pend[id], true, nil }
	a.e.Scan = func(_ context.Context, id string) (protocol.PatchScanReport, bool, error) {
		return protocol.PatchScanReport{Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}}}, true, nil
	}
	c := campaign.Campaign{Name: "tudo de novembro", Type: campaign.TypeOSAll, Rings: []int{0, 1, 2}, TTL: 24 * time.Hour, CreatedBy: "t"}
	if c.Target() != "todas as atualizações do sistema aprovadas em Patch" {
		t.Errorf("alvo: %q", c.Target())
	}
	bad := c
	bad.Sources = []string{"curl"}
	if bad.Validate() == nil {
		t.Error("a de tudo não leva fontes")
	}
	id := a.cria(c)
	pub := a.e.Key.Public().(ed25519.PublicKey)

	a.passo(id, 0)
	env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
	j, err := jobs.Verify(pub, env[0], "p1", a.agora, 0)
	up, _ := jobs.ParseUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeUpdate || strings.Join(up.Names(), ",") != "bluez,libbluetooth3,curl" {
		t.Fatalf("Linux: %+v %+v %v", j, up, err)
	}
	a.encerra(id, 0, jobs.StateDone)
	c = a.passo(id, time.Minute)
	l, _ := a.st.CampaignJobs(ctx, id, 1)
	if len(l) != 1 || l[0].MachineID != "a1" || c.Notes[1] != "grupo de implantação 1: 1 job(s); fora: 1 sem instalação pelo patchd (macOS)" {
		t.Fatalf("grupo 1: %+v %q", l, c.Notes)
	}
	env, _ = a.st.DeliverJobs(ctx, "a1", a.agora)
	j, err = jobs.Verify(pub, env[0], "a1", a.agora, 0)
	wp, _ := jobs.ParseWinUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeWinUpdate || len(wp.Updates) != 1 || wp.Updates[0].KB != "5127648" {
		t.Fatalf("Windows: %+v %+v %v", j, wp, err)
	}
	a.encerra(id, 1, jobs.StateDone)
	c = a.passo(id, 2*time.Minute)
	if c.State != campaign.StateDone || c.Notes[2] != "grupo de implantação 2: 0 job(s); fora: 1 sem pendência aprovada" {
		t.Errorf("fim: %s %q", c.State, c.Notes)
	}
}

// catalogoFalso: o planejador de terceiros com a lista dos aprovados.
type catalogoFalso struct {
	planejadorFalso
	aprovados []campaign.AppVersion
	pronto    map[string]string          // programa → motivo de não estar pronto
	tem       map[string]map[string]bool // máquina → programas instalados em versão antiga
}

func (c *catalogoFalso) Approved(context.Context) ([]campaign.AppVersion, error) {
	return c.aprovados, nil
}

func (c *catalogoFalso) Ready(_ context.Context, app, _ string) (string, error) {
	return c.pronto[app], nil
}

func (c *catalogoFalso) Job(ctx context.Context, id, app, version string) (*jobs.ThirdPartyParams, string, error) {
	if !c.tem[id][app] {
		return nil, "sem o programa", nil
	}
	return c.planejadorFalso.Job(ctx, id, app, version)
}

// Todos os programas aprovados: um job por programa que a máquina tem em versão antiga; o não
// pronto fica nas notas; o adiado tenta de novo só aquele programa.
func TestCampanhaTodosOsProgramas(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	cat := &catalogoFalso{
		aprovados: []campaign.AppVersion{{App: "7zip", Version: "26.04"}, {App: "chrome", Version: "142.0"}, {App: "vscode", Version: "1.141.0"}},
		pronto:    map[string]string{"vscode": "nenhum instalador guardado"},
		tem:       map[string]map[string]bool{"p1": {"7zip": true, "chrome": true, "vscode": true}, "a1": {"chrome": true}},
	}
	a.e.ThirdParty = cat
	id := a.cria(campaign.Campaign{Name: "programas", Type: campaign.TypeThirdPartyAll, Rings: []int{0, 1, 2}, TTL: 24 * time.Hour, CreatedBy: "t"})

	c := a.passo(id, 0)
	l, _ := a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 2 || l[0].Slot != "7zip" || l[1].Slot != "chrome" || c.Notes[0] != "grupo de implantação 0: 2 job(s); fora: 1 vscode 1.141.0 não pronto" {
		t.Fatalf("piloto: %+v %q", l, c.Notes)
	}
	if !strings.Contains(c.Detail, "2 de 2 job(s) em aberto") {
		t.Errorf("detalhe: %s", c.Detail)
	}
	// O 7zip conclui; o chrome é adiado (programa aberto): a nova tentativa é só do chrome.
	if ok, _ := a.st.FinishJob(ctx, l[0].ID, jobs.StateDone, "ok", a.agora); !ok {
		t.Fatal("encerrar")
	}
	if ok, _ := a.st.FinishJob(ctx, l[1].ID, jobs.StatePostponed, "chrome aberto", a.agora); !ok {
		t.Fatal("encerrar")
	}
	c = a.passo(id, campaign.RetryAfter+time.Minute)
	l, _ = a.st.CampaignJobs(ctx, id, 0)
	if len(l) != 3 || l[2].Slot != "chrome" || l[2].State != jobs.StatePending || !strings.Contains(c.Detail, "1 de 2 job(s) em aberto") {
		t.Fatalf("nova tentativa: %+v %s", l, c.Detail)
	}
	if ok, _ := a.st.FinishJob(ctx, l[2].ID, jobs.StateDone, "ok", a.agora); !ok {
		t.Fatal("encerrar")
	}
	c = a.passo(id, campaign.RetryAfter+2*time.Minute)
	if c.CurrentIdx != 1 {
		t.Fatalf("grupo 1 não liberado: %+v", c)
	}
	l, _ = a.st.CampaignJobs(ctx, id, 1)
	if len(l) != 1 || l[0].MachineID != "a1" || l[0].Slot != "chrome" || c.Notes[1] != "grupo de implantação 1: 1 job(s); fora: 1 nenhum programa aprovado para atualizar, 1 vscode 1.141.0 não pronto" {
		t.Fatalf("grupo 1: %+v %q", l, c.Notes)
	}
	if got := (campaign.Campaign{Type: campaign.TypeThirdPartyAll}).Target(); !strings.Contains(got, "todos os programas de terceiros aprovados") {
		t.Errorf("alvo: %q", got)
	}
	// Sem catálogo (planejador antigo): nada é criado, e as notas dizem por quê.
	b := novoAmbiente(t)
	b.e.ThirdParty = &planejadorFalso{}
	id2 := b.cria(campaign.Campaign{Name: "x", Type: campaign.TypeThirdPartyAll, Rings: []int{0}, TTL: time.Hour, CreatedBy: "t"})
	if c := b.passo(id2, 0); c.State != campaign.StateDone || !strings.Contains(c.Notes[0], "sem as regras de terceiros") {
		t.Errorf("sem catálogo: %+v", c)
	}
}
