package agent

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/wol"
)

// docs/wake-on-lan.md, Aula 22.2: o repetidor recebe o job assinado, manda o pacote pelo
// broadcast da rede pedida e conta por onde foi; a rede que não é dele vira recusa.
func TestLigarPelaRedePorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	var enviados []string
	s := &wol.Sender{
		Ifaces: func() ([]wol.Iface, error) {
			return []wol.Iface{{Name: "eth0", Prefix: netip.MustParsePrefix("192.0.2.10/24")}}, nil
		},
		Send: func(_ context.Context, from, to netip.Addr, p []byte) error {
			enviados = append(enviados, from.String()+">"+to.String())
			return nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	ctx := context.Background()
	a.CheckIn(ctx) // o primeiro ciclo (a busca diária)

	params := func(network string) func(*jobs.Job) {
		return func(j *jobs.Job) {
			j.Type = jobs.TypeWakeOnLAN
			j.Params, _ = jobs.WakeOnLANParamsJSON(jobs.WakeOnLANParams{Target: "alvo-1", MACs: []string{"00:11:22:33:44:55"}, Network: network})
		}
	}
	// Sem o executor: recusa.
	j := criaJob(t, srv, priv, id, params("192.0.2.0/24"))
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "pacote mágico") {
		t.Fatalf("sem executor: %+v", e)
	}

	a.WakeOnLAN = WakeOnLANRunner(s)
	j = criaJob(t, srv, priv, id, params("192.0.2.0/24"))
	a.CheckIn(ctx)
	e := estadoDoJob(srv, id, j)
	if e.State != jobs.StateDone || !strings.Contains(e.Detail, "192.0.2.255") || !strings.Contains(e.Detail, "alvo-1") {
		t.Fatalf("job: %+v\n%s", e, log.String())
	}
	if len(enviados) != wol.Repeats || enviados[0] != "192.0.2.10>192.0.2.255" {
		t.Fatalf("enviados %v", enviados)
	}
	if buscas.Load() != 1 {
		t.Error("ligar outra máquina não força a busca desta")
	}

	// Rede que não é desta máquina: rejeitado, nada sai.
	enviados = nil
	j = criaJob(t, srv, priv, id, params("198.51.100.0/24"))
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "198.51.100.0/24") || len(enviados) != 0 {
		t.Fatalf("rede alheia: %+v, %v", e, enviados)
	}

	// Falha da rede: falhou.
	s.Send = func(context.Context, netip.Addr, netip.Addr, []byte) error { return errors.New("sem rota") }
	j = criaJob(t, srv, priv, id, params("192.0.2.0/24"))
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "sem rota") {
		t.Fatalf("falha: %+v", e)
	}
}
