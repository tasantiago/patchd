package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
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

func TestUpdatePorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()
	a.CheckIn(ctx) // a busca diária sai do caminho

	// A busca devolve o que o teste mandar: o efeito da instalação aparece nela.
	pendentes := []protocol.MissingUpdate{}
	a.Scan = func(context.Context) protocol.PatchScanReport {
		buscas.Add(1)
		return protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
			Scans: []protocol.ScanResult{{Source: "apt", Missing: pendentes}}}
	}
	var pedidos [][]jobs.Package
	var resposta error
	a.Install = func(_ context.Context, _ int64, pkgs []jobs.Package) (string, error) {
		pedidos = append(pedidos, pkgs)
		return "apt-get: 1 upgraded", resposta
	}
	update := func(j *jobs.Job) {
		j.Type = jobs.TypeUpdate
		j.Params = []byte(`{"packages":[{"name":"libcares2","min_version":"1.34.6-1ubuntu0.1"}]}`)
	}

	// 1. Instalou, e a busca seguinte não mostra mais o pacote: concluído.
	j1 := criaJob(t, srv, priv, id, update)
	if out := a.CheckIn(ctx); out != outcomeOK || len(pedidos) != 1 || pedidos[0][0].Name != "libcares2" || buscas.Load() != 2 {
		t.Fatalf("update: %v %v %d\n%s", out, pedidos, buscas.Load(), log.String())
	}
	if e := estadoDoJob(srv, id, j1); e.State != jobs.StateDone || !strings.Contains(e.Detail, "1 pacote(s) conferidos") {
		t.Fatalf("job 1: %+v\n%s", e, log.String())
	}

	// 2. O agente disse ok, mas a busca ainda mostra a versão velha instalada: falhou.
	pendentes = []protocol.MissingUpdate{{ID: "libcares2", Package: "libcares2", InstalledVersion: "1.34.6-1", FixedVersion: "1.34.6-1ubuntu0.1", Source: "apt"}}
	j2 := criaJob(t, srv, priv, id, update)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j2); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "libcares2 instalada 1.34.6-1") {
		t.Errorf("job 2: %+v", e)
	}

	// 3. O instalador recusa (pacote não pendente): rejeitado, com o motivo.
	resposta = fmt.Errorf("%w: bash não está pendente", jobs.ErrNotPending)
	j3 := criaJob(t, srv, priv, id, update)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j3); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "bash não está pendente") {
		t.Errorf("job 3: %+v", e)
	}

	// 4. A instalação falha: falhou, com a saída.
	resposta = errors.New("apt-get: exit status 100")
	j4 := criaJob(t, srv, priv, id, update)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j4); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "exit status 100") {
		t.Errorf("job 4: %+v", e)
	}

	// 5. Sistema sem instalador (Windows, macOS): rejeitado sem tentar.
	a.Install = nil
	j5 := criaJob(t, srv, priv, id, update)
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j5); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "só Linux") || len(pedidos) != 4 {
		t.Errorf("job 5: %+v (pedidos %d)", e, len(pedidos))
	}
}
