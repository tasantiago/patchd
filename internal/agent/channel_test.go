package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/canal"
	"github.com/tasantiago/patchd/internal/jobs"
)

// O canal em tempo real de ponta a ponta: o check-in liga a espera; um job criado para a
// máquina acorda o agente em segundos (o check-in antecipado com o motivo "canal").
func TestCanalEmTempoReal(t *testing.T) {
	hub := canal.New()
	srv, id, cred := novoServidor(t, api.WithChannel(hub))
	srv.st.OnJobCreated(func(m string) { hub.Notify(m, "job") })
	_, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID = id
	a.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.channelLoop(ctx)

	if a.CheckIn(ctx); !a.channelOn.Load() {
		t.Fatal("o check-in não ligou o canal")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !hub.IsConnected(id) {
		if time.Now().After(deadline) {
			t.Fatal("o agente não abriu a espera")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	criaJob(t, srv, priv, id, nil)
	select {
	case <-a.wakeChan():
	case <-time.After(5 * time.Second):
		t.Fatal("o job não acordou o agente")
	}
	if r, _ := a.wakeReason.Load().(string); r != ReasonChannel || time.Since(start) > 2*time.Second {
		t.Errorf("motivo %q em %v", r, time.Since(start))
	}
	// A espera volta a abrir depois do aviso.
	for !hub.IsConnected(id) {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatal("a espera não voltou")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Desligado no servidor (sem hub): o check-in desliga a espera.
	srv2, _, cred2 := novoServidor(t)
	b := agenteDeTeste(t, srv2, cred2, &buscas, &bytes.Buffer{})
	b.setChannel(true)
	if b.CheckIn(ctx); b.channelOn.Load() {
		t.Error("servidor sem canal: a espera continuou ligada")
	}
}

// A pergunta ao parque: o agente responde e o resultado vai já, no mesmo ciclo; sem os
// sensores, recusa.
func TestPerguntaAoParqueNoAgente(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()
	a.CheckIn(ctx)
	raw := json.RawMessage(`{"sensor":"processo","arg":"firefox.exe"}`)
	var pedidas []jobs.QueryParams
	a.Query = func(_ context.Context, p jobs.QueryParams) jobs.QueryAnswer {
		pedidas = append(pedidas, p)
		return jobs.QueryAnswer{Value: "rodando", Extra: "2 processo(s)"}
	}
	job := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeQuery, raw })
	a.CheckIn(ctx)
	row, _, _ := srv.st.JobForMachine(ctx, id, job)
	if len(pedidas) != 1 || row.State != jobs.StateDone || jobs.ParseAnswer(row.Detail) != (jobs.QueryAnswer{Value: "rodando", Extra: "2 processo(s)"}) {
		t.Fatalf("resposta: %+v %s %q", pedidas, row.State, row.Detail)
	}
	a.Query = nil
	job = criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeQuery, raw })
	a.CheckIn(ctx)
	if row, _, _ := srv.st.JobForMachine(ctx, id, job); row.State != jobs.StateRejected || !strings.Contains(row.Detail, "não responde perguntas") {
		t.Errorf("sem sensores: %s %q", row.State, row.Detail)
	}
}
