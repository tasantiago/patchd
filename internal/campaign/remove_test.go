package campaign_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// A campanha de remoção: cada grupo recebe o job de desinstalar onde o inventário mostra o
// programa para a máquina toda e com desinstalação silenciosa; os outros ficam de fora, com o
// motivo.
func TestCampanhaDeRemocao(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	const chave = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{11111111-2222-3333-4444-555555555555}`
	win := func(sw ...protocol.Software) protocol.InventoryReport {
		return protocol.InventoryReport{OS: protocol.OSInfo{Family: "windows"}, Software: sw}
	}
	msi := protocol.Software{Name: "Leitor X 3", Version: "3.1", Publisher: "Fab", Scope: "machine", Uninstall: "msi", ProductCode: "{11111111-2222-3333-4444-555555555555}", Source: chave}
	inv := map[string]protocol.InventoryReport{
		"p1": win(msi),
		"a1": win(protocol.Software{Name: "Leitor X 3", Version: "3.1", Publisher: "Fab", Scope: "user", Uninstall: "quiet", Source: `HKU\S-1\Software\Microsoft\Windows\CurrentVersion\Uninstall\U`}),
		"a2": win(msi),
		"b1": {OS: protocol.OSInfo{Family: "linux"}},
	}
	novo := map[string]bool{"p1": true, "b1": true}
	a.e.Inventory = func(_ context.Context, id string) (protocol.InventoryReport, bool, error) {
		r, ok := inv[id]
		return r, ok, nil
	}
	a.e.CanUninstall = func(_ context.Context, id string) (bool, error) { return novo[id], nil }

	alvo := &campaign.RemoveTarget{Program: "Leitor X", Publisher: "Fab", Names: []string{"Leitor X 3"}}
	for _, c := range []campaign.Campaign{
		{Name: "r", Type: jobs.TypeUninstall, Rings: []int{0}, TTL: time.Hour},
		{Name: "r", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, Remove: alvo},
		{Name: "r", Type: jobs.TypeUninstall, Rings: []int{0}, TTL: time.Hour, Remove: &campaign.RemoveTarget{Program: "Leitor X"}},
		{Name: "r", Type: jobs.TypeUninstall, Rings: []int{0}, TTL: time.Hour, Remove: &campaign.RemoveTarget{Names: []string{"x"}}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("deveria recusar: %+v", c)
		}
	}
	if (campaign.RemoveTarget{Program: "Leitor X", Names: []string{"Leitor X 3"}, Versions: []string{"3.0"}}).Matches(msi) {
		t.Error("versão fora da escolha")
	}

	id := a.cria(campaign.Campaign{Name: "remover leitor", Type: jobs.TypeUninstall, Rings: []int{0, 1, 2}, TTL: 6 * time.Hour, Remove: alvo, CreatedBy: "thyago"})
	c := a.passo(id, 0)
	if c.Notes[0] != "grupo de implantação 0: 1 job(s)" || c.Target() != "remover Leitor X (Fab), todas as versões" {
		t.Fatalf("grupo 0: %q %s", c.Notes, c.Target())
	}
	env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
	j, err := jobs.Verify(a.e.Key.Public().(ed25519.PublicKey), env[0], "p1", a.agora, 0)
	if err != nil || j.Type != jobs.TypeUninstall {
		t.Fatalf("job: %+v %v", j, err)
	}
	p, err := jobs.ParseUninstallParams(j.Params)
	if err != nil || p.Name != "Leitor X 3" || p.Method != "msi" || p.ProductCode != "{11111111-2222-3333-4444-555555555555}" || p.Key != chave {
		t.Fatalf("parâmetros: %+v %v", p, err)
	}
	a.encerra(id, 0, jobs.StateDone)
	// Os grupos 1 e 2 não têm o que remover pelo patchd: passam direto, com o motivo.
	c = a.passo(id, 25*time.Hour)
	if c.State != campaign.StateDone || c.Notes[1] != "grupo de implantação 1: 0 job(s); fora: 1 agente sem a remoção (atualize o agente), 1 instalação por usuário" ||
		c.Notes[2] != "grupo de implantação 2: 0 job(s); fora: 1 não é Windows" {
		t.Errorf("fim: %s %q", c.State, c.Notes)
	}
}
