package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func criaJob(t *testing.T, srv *servidor, priv ed25519.PrivateKey, maq string, ajuste func(*jobs.Job)) int64 {
	t.Helper()
	ctx := context.Background()
	id, _ := srv.st.NextJobID(ctx)
	agora := time.Now().UTC()
	j := jobs.Job{ID: id, MachineID: maq, Type: jobs.TypeRescan, IssuedAt: agora, NotAfter: agora.Add(time.Hour)}
	if ajuste != nil {
		ajuste(&j)
	}
	env, err := jobs.Sign(priv, j)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.st.CreateJob(ctx, store.NewJob{ID: id, MachineID: maq, Type: j.Type, Signed: env, CreatedBy: "teste", NotAfter: j.NotAfter}); err != nil {
		t.Fatal(err)
	}
	return id
}

func estadoDoJob(srv *servidor, maq string, id int64) store.JobRow {
	j, _, _ := srv.st.JobForMachine(context.Background(), maq, id)
	return j
}

func TestRescanPorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()

	// Primeiro ciclo: a busca diária, sem job.
	if out := a.CheckIn(ctx); out != outcomeOK || buscas.Load() != 1 {
		t.Fatalf("primeiro ciclo: %v %d", out, buscas.Load())
	}
	// Segundo ciclo, sem job: só check-in, a busca não está devida.
	a.CheckIn(ctx)
	if buscas.Load() != 1 {
		t.Fatal("a busca diária não se repete")
	}
	// Com um job de rescan: a busca roda já neste ciclo, e o servidor confere pela busca.
	j := criaJob(t, srv, priv, id, nil)
	if out := a.CheckIn(ctx); out != outcomeOK || buscas.Load() != 2 {
		t.Fatalf("rescan: %v %d", out, buscas.Load())
	}
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone {
		t.Fatalf("job: %+v\n%s", e, log.String())
	}
	st, _ := loadState(a.DataDir)
	if st.LastJobID != j || len(st.PendingJobResults) != 0 {
		t.Errorf("estado: %+v", st)
	}
}

func TestJobsRecusados(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	_, outra, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()
	a.CheckIn(ctx) // busca diária feita

	// Assinado com outra chave (servidor falso, ou chave trocada): recusado, sem busca.
	jOutra := criaJob(t, srv, outra, id, nil)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, jOutra); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "assinatura inválida") || buscas.Load() != 1 {
		t.Errorf("outra chave: %+v (buscas %d)", e, buscas.Load())
	}

	// Agente sem chave de jobs: recusa e diz por quê.
	a.JobKey = nil
	jSemChave := criaJob(t, srv, priv, id, nil)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, jSemChave); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "PATCHD_JOB_PUBKEY") {
		t.Errorf("sem chave: %+v", e)
	}
	a.JobKey = pub

	// Vencido para o relógio desta máquina (o servidor ainda o entregou): recusado.
	jVelho := criaJob(t, srv, priv, id, nil)
	a.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	a.CheckIn(ctx)
	a.now = time.Now
	if e := estadoDoJob(srv, id, jVelho); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "vencido") {
		t.Errorf("vencido: %+v", e)
	}
	if buscas.Load() != 1 {
		t.Errorf("nenhum job recusado roda a busca: %d", buscas.Load())
	}
}

func TestResultadoGuardadoComServidorFora(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()
	a.CheckIn(ctx)

	j := criaJob(t, srv, priv, id, nil)
	// O servidor cai durante a busca do rescan: a busca e o resultado ficam guardados.
	busca := a.Scan
	a.Scan = func(ctx context.Context) protocol.PatchScanReport {
		rep := busca(ctx)
		srv.fora.Store(true)
		return rep
	}
	if out := a.CheckIn(ctx); out != outcomeTransient {
		t.Fatalf("com o servidor fora: %v", out)
	}
	st, _ := loadState(a.DataDir)
	if len(st.PendingJobResults) != 1 || st.PendingJobResults[0].ID != j || st.LastJobID != j {
		t.Fatalf("guardado: %+v", st)
	}
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDelivered {
		t.Fatalf("no servidor, ainda entregue: %+v", e)
	}

	// O servidor volta: a busca pendente vai primeiro, depois o resultado, e o job fecha.
	srv.fora.Store(false)
	a.Scan = busca
	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("volta: %v", out)
	}
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone {
		t.Errorf("concluído depois da volta: %+v", e)
	}
	if buscas.Load() != 2 {
		t.Errorf("a busca não se repete: %d", buscas.Load())
	}
}
