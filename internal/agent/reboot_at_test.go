package agent

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 17.6: o reinício na hora escolhida pelo usuário — o agente espera até ela, e o
// estado guarda essa hora para conferir depois se a máquina reiniciou.
func TestReinicioNaHoraEscolhida(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	sim := true
	boot := time.Now().UTC().Add(-time.Hour)
	a.RebootStatus = func(context.Context) (protocol.RebootStatus, error) {
		return protocol.RebootStatus{Pending: &sim, LastBoot: &boot}, nil
	}
	var espera time.Duration
	a.Restart = func(_ context.Context, _ int64, d time.Duration) error { espera = d; return nil }
	ctx := context.Background()
	a.CheckIn(ctx)
	at := time.Now().Add(3 * time.Hour)
	raw, err := jobs.RebootAtParamsJSON(at)
	if err != nil {
		t.Fatal(err)
	}
	criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeReboot, raw })
	a.CheckIn(ctx)
	if espera < 3*time.Hour-time.Minute || espera > 3*time.Hour {
		t.Fatalf("espera até a hora escolhida: %v", espera)
	}
	st, _ := loadState(a.DataDir)
	if st.PendingReboot == nil || st.PendingReboot.At.Sub(at.UTC()).Abs() > time.Minute {
		t.Errorf("estado: %+v", st.PendingReboot)
	}
}

// Aula 17.6: dois reinícios — vale o mais cedo; o outro volta dispensado.
func TestReinicioMaisCedoVale(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	sim := true
	boot := time.Now().UTC().Add(-time.Hour)
	a.RebootStatus = func(context.Context) (protocol.RebootStatus, error) {
		return protocol.RebootStatus{Pending: &sim, LastBoot: &boot}, nil
	}
	var agendados []time.Duration
	cancelados := 0
	a.Restart = func(_ context.Context, _ int64, d time.Duration) error { agendados = append(agendados, d); return nil }
	a.AbortRestart = func(context.Context) error { cancelados++; return nil }
	ctx := context.Background()
	a.CheckIn(ctx)

	// O usuário escolheu daqui a 3 h.
	raw, _ := jobs.RebootAtParamsJSON(time.Now().Add(3 * time.Hour))
	doUsuario := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeReboot, raw })
	a.CheckIn(ctx)
	// A TI pede com 15 min de aviso: mais cedo, toma o lugar; o do usuário fica atendido.
	raw15, _ := jobs.RebootParamsJSON(15 * time.Minute)
	daTI := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeReboot, raw15 })
	a.CheckIn(ctx)
	a.CheckIn(ctx) // o resultado do substituído vai no ciclo seguinte
	if cancelados != 1 || len(agendados) != 2 || agendados[1] != 15*time.Minute {
		t.Fatalf("cancelados %d, agendados %v", cancelados, agendados)
	}
	if e := estadoDoJob(srv, id, doUsuario); e.State != jobs.StateDismissed || !strings.Contains(e.Detail, "já havia um reinício agendado para antes") {
		t.Errorf("o do usuário: %+v", e)
	}
	st, _ := loadState(a.DataDir)
	if st.PendingReboot == nil || st.PendingReboot.JobID != daTI {
		t.Errorf("pendente: %+v", st.PendingReboot)
	}
	// Outro pedido para depois: já atendido pelo de agora.
	raw5, _ := jobs.RebootAtParamsJSON(time.Now().Add(5 * time.Hour))
	depois := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeReboot, raw5 })
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, depois); e.State != jobs.StateDismissed || cancelados != 1 || len(agendados) != 2 {
		t.Errorf("o de depois: %+v (cancelados %d, agendados %v)", e, cancelados, agendados)
	}
}
