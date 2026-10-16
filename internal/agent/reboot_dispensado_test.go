package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 16.5: reinício pedido a uma máquina que já não tem reinício pendente volta
// "dispensado" (nada a fazer), não "rejeitado": numa campanha, não pausa o anel.
func TestReinicioSemPendenciaViraDispensado(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	nao := false
	boot := time.Now().UTC().Add(-time.Hour)
	a.RebootStatus = func(context.Context) (protocol.RebootStatus, error) {
		return protocol.RebootStatus{Pending: &nao, LastBoot: &boot}, nil
	}
	reiniciou := false
	a.Restart = func(context.Context, int64, time.Duration) error { reiniciou = true; return nil }
	ctx := context.Background()
	a.CheckIn(ctx)
	raw, _ := json.Marshal(jobs.RebootParams{DelayMinutes: 15})
	j := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeReboot, raw })
	a.CheckIn(ctx)
	e := estadoDoJob(srv, id, j)
	if e.State != jobs.StateDismissed || e.Detail != "dispensado: a máquina já não tinha reinício pendente (o agente conferiu na hora)" || reiniciou {
		t.Errorf("job: %+v (reiniciou %v)", e, reiniciou)
	}
}
