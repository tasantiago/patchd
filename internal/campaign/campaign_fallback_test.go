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

// A reserva dentro do job: a política oferece o KB hoje; com a opção na campanha, a reserva
// ligada e um agente que sabe (InJob) numa máquina que permite, o job da política leva o
// Fallback. Agente anterior, máquina bloqueada ou campanha sem a opção: o job de sempre.
func TestReservaNoJobDaPolitica(t *testing.T) {
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	oferece := protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}}}}
	casos := []struct {
		nome   string
		ms     *protocol.MicrosoftUpdate
		opcao  bool
		ligada bool
		quer   bool
	}{
		{"agente novo", &protocol.MicrosoftUpdate{Available: true, InJob: true}, true, true, true},
		{"agente anterior", &protocol.MicrosoftUpdate{Available: true}, true, true, false},
		{"GPO bloqueia", &protocol.MicrosoftUpdate{InJob: true, Reason: "GPO"}, true, true, false},
		{"sem a opção", &protocol.MicrosoftUpdate{Available: true, InJob: true}, false, true, false},
		{"reserva desligada", &protocol.MicrosoftUpdate{Available: true, InJob: true}, true, false, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			a := novoAmbiente(t)
			ctx := context.Background()
			a.e.Pending = func(context.Context, string) ([]protocol.PendingItem, bool, error) {
				return []protocol.PendingItem{{ID: "CVE-2026-1", FixedIn: "26300.9711 (KB5127648)"}}, true, nil
			}
			a.e.Scan = func(context.Context, string) (protocol.PatchScanReport, bool, error) {
				return protocol.PatchScanReport{MicrosoftUpdate: c.ms, Scans: []protocol.ScanResult{oferece}}, true, nil
			}
			a.e.MicrosoftAllowed = func() bool { return c.ligada }
			a.passo(a.cria(campaign.Campaign{Name: "x", Type: jobs.TypeWinUpdate, Sources: []string{"KB5127648"}, Rings: []int{0},
				TTL: 6 * time.Hour, CreatedBy: "t", MicrosoftFallback: c.opcao}), 0)
			env, _ := a.st.DeliverJobs(ctx, "p1", a.agora)
			if len(env) != 1 {
				t.Fatalf("%d job(s)", len(env))
			}
			j, err := jobs.Verify(a.e.Key.Public().(ed25519.PublicKey), env[0], "p1", a.agora, 0)
			if err != nil {
				t.Fatal(err)
			}
			p, err := jobs.ParseWinUpdateParams(j.Params)
			if err != nil || p.Source != jobs.WinSourcePolicy || p.Fallback != c.quer || p.Updates[0].ID != cu {
				t.Errorf("%+v %v", p, err)
			}
		})
	}
}
