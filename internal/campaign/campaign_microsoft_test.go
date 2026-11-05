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

// A fonte reserva: com a opção na campanha e a reserva ligada em Parâmetros, quem ficaria de
// fora por um motivo que a Microsoft resolve (fonte da política falhando, KB fora da busca)
// recebe o job com a fonte Microsoft. Desligada, fica de fora como antes, com o aviso; a
// máquina com a GPO que bloqueia a Microsoft também.
func TestCampanhaDoWindowsComReservaMicrosoft(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	fix := "26300.9711 (KB5127648)"
	pend := map[string][]protocol.PendingItem{}
	for _, m := range []string{"p1", "a1", "a2", "b1"} {
		pend[m] = []protocol.PendingItem{{ID: "CVE-2026-1", FixedIn: fix}}
	}
	livre := &protocol.MicrosoftUpdate{Available: true}
	falha := protocol.ScanResult{Source: "wua-default", Error: "Search: HRESULT 0x80244007"}
	scans := map[string]protocol.PatchScanReport{
		"p1": {MicrosoftUpdate: livre, Scans: []protocol.ScanResult{falha}},
		// a1: a política falha; a busca direta na Microsoft mostra o UpdateID.
		"a1": {MicrosoftUpdate: livre, Scans: []protocol.ScanResult{falha,
			{Source: "wua-microsoft", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}}},
		// a2: a política responde, mas não oferece o KB: vai só o KB, que o agente procura na Microsoft.
		"a2": {MicrosoftUpdate: livre, Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{}}}},
		"b1": {MicrosoftUpdate: &protocol.MicrosoftUpdate{Reason: "GPO DoNotConnectToWindowsUpdateInternetLocations"}, Scans: []protocol.ScanResult{falha}},
	}
	a.e.Pending = func(_ context.Context, id string) ([]protocol.PendingItem, bool, error) { return pend[id], true, nil }
	a.e.Scan = func(_ context.Context, id string) (protocol.PatchScanReport, bool, error) {
		r, ok := scans[id]
		return r, ok, nil
	}
	ligada := false
	a.e.MicrosoftAllowed = func() bool { return ligada }

	if err := (campaign.Campaign{Name: "x", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, MicrosoftFallback: true}).Validate(); err == nil {
		t.Error("a reserva só vale nas campanhas do Windows")
	}
	nova := func(rings ...int) int64 {
		return a.cria(campaign.Campaign{Name: "outubro", Type: jobs.TypeWinUpdate, Sources: []string{"KB5127648"}, Rings: rings,
			TTL: 6 * time.Hour, CreatedBy: "thyago", MicrosoftFallback: true})
	}

	// A reserva desligada em Parâmetros: fica de fora, e a nota diz por quê.
	c := a.passo(nova(0), 0)
	if c.Notes[0] != "grupo de implantação 0: 0 job(s); fora: 1 fonte da política falhando (reserva da Microsoft desligada em Parâmetros)" {
		t.Fatalf("desligada: %q", c.Notes)
	}

	ligada = true
	id := nova(1, 2)
	c = a.passo(id, 30*time.Hour)
	if c.Notes[0] != "grupo de implantação 1: 2 job(s)" {
		t.Fatalf("grupo 1: %q", c.Notes)
	}
	pub := a.e.Key.Public().(ed25519.PublicKey)
	params := func(m string) jobs.WinUpdateParams {
		t.Helper()
		env, _ := a.st.DeliverJobs(ctx, m, a.agora.Add(30*time.Hour))
		if len(env) != 1 {
			t.Fatalf("%s: %d job(s)", m, len(env))
		}
		j, err := jobs.Verify(pub, env[0], m, a.agora.Add(30*time.Hour), 0)
		if err != nil {
			t.Fatal(err)
		}
		p, err := jobs.ParseWinUpdateParams(j.Params)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := params("a1"); p.Source != jobs.WinSourceMicrosoft || len(p.Updates) != 1 || p.Updates[0].ID != cu || p.Updates[0].KB != "5127648" {
		t.Errorf("a1: %+v", p)
	}
	if p := params("a2"); p.Source != jobs.WinSourceMicrosoft || len(p.Updates) != 1 || p.Updates[0].ID != "" || p.Updates[0].KB != "5127648" {
		t.Errorf("a2: %+v", p)
	}
	a.encerra(id, 0, jobs.StateDone, jobs.StateDone)

	// Grupo 2: a GPO da máquina bloqueia a Microsoft.
	c = a.passo(id, 60*time.Hour)
	if c.State != campaign.StateDone || !strings.HasPrefix(c.Notes[1], "grupo de implantação 2: 0 job(s); fora: 1 Microsoft bloqueada: GPO") {
		t.Errorf("fim: %s %q", c.State, c.Notes)
	}
}
