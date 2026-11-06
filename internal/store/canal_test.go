package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O canal no PostgreSQL: todo job criado avisa pelo NOTIFY (depois do COMMIT); os agendados
// que vencem saem na consulta; e as perguntas guardam os jobs delas.
func TestCanalEPerguntasPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	for _, m := range []string{"canal-1", "canal-2"} {
		if _, err := st.SaveInventory(ctx, m, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + m}); err != nil {
			t.Fatal(err)
		}
	}
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	got := make(chan string, 10)
	done := make(chan error, 1)
	go func() { done <- st.ListenJobs(lctx, func() { close(ready) }, func(id string) { got <- id }) }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("LISTEN não começou")
	}
	qid, err := st.CreateQuestion(ctx, Question{Sensor: "processo", Arg: "x.exe", Target: "todas as máquinas", Machines: 2, CreatedBy: "t"})
	if err != nil {
		t.Fatal(err)
	}
	cria := func(m string, notBefore time.Time) int64 {
		t.Helper()
		id, _ := st.NextJobID(ctx)
		if err := st.CreateJob(ctx, NewJob{ID: id, MachineID: m, Type: jobs.TypeQuery, Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "t", NotAfter: time.Now().Add(2 * time.Hour), NotBefore: notBefore, QuestionID: qid}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	j1 := cria("canal-1", time.Time{})
	select {
	case id := <-got:
		if id != "canal-1" {
			t.Errorf("aviso de %q", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("o job novo não avisou")
	}
	// Agendado: avisa ao criar e sai na consulta quando o início chega.
	inicio := time.Now().Add(time.Minute).Truncate(time.Second)
	cria("canal-2", inicio)
	<-got
	if ids, _ := st.DueJobMachines(ctx, inicio.Add(-time.Second), inicio); len(ids) != 1 || ids[0] != "canal-2" {
		t.Errorf("vencidos: %v", ids)
	}
	if ids, _ := st.DueJobMachines(ctx, inicio, inicio.Add(time.Minute)); len(ids) != 0 {
		t.Errorf("fora do intervalo: %v", ids)
	}
	// A pergunta: os jobs dela, e as respondidas.
	st.DeliverJobs(ctx, "canal-1", time.Now())
	st.FinishJob(ctx, j1, jobs.StateDone, `{"value":"rodando"}`, time.Now())
	q, ok, _ := st.Question(ctx, qid)
	if !ok || q.Answered != 1 || q.Machines != 2 {
		t.Errorf("pergunta: %+v", q)
	}
	if l, _ := st.QuestionJobs(ctx, qid); len(l) != 2 || l[0].MachineID != "canal-1" || l[0].Detail != `{"value":"rodando"}` {
		t.Errorf("jobs da pergunta: %+v", l)
	}
	if l, _ := st.Questions(ctx, 5); len(l) != 1 || l[0].Answered != 1 {
		t.Errorf("perguntas: %+v", l)
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("ListenJobs terminou sem erro com o contexto cancelado")
	}
}
