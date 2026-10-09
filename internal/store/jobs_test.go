package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoJobs interface {
	SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error)
	SaveScan(ctx context.Context, id string, rep protocol.PatchScanReport) error
	NextJobID(ctx context.Context) (int64, error)
	CreateJob(ctx context.Context, j NewJob) error
	ExpireJobs(ctx context.Context, now time.Time) (int64, error)
	DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error)
	JobForMachine(ctx context.Context, machineID string, id int64) (JobRow, bool, error)
	FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error)
	ScanReceivedAt(ctx context.Context, machineID string) (*time.Time, error)
	Jobs(ctx context.Context, machineID string, limit int) ([]JobRow, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
}

func testarJobs(t *testing.T, st contratoJobs) {
	ctx := context.Background()
	agora := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"maq-a", "maq-b"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	if at, err := st.ScanReceivedAt(ctx, "maq-a"); at != nil || err != nil {
		t.Errorf("sem busca: %v %v", at, err)
	}
	cria := func(maq string, prazo time.Duration) int64 {
		t.Helper()
		id, err := st.NextJobID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		env := protocol.SignedJob{Payload: `{"id":` + itoa(id) + `}`, Signature: "sig"}
		if err := st.CreateJob(ctx, NewJob{ID: id, MachineID: maq, Type: "rescan", Signed: env, CreatedBy: "thyago", NotAfter: agora.Add(prazo)}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	j1, j2, jOutra, jVencido, jCancelar := cria("maq-a", time.Hour), cria("maq-a", time.Hour), cria("maq-b", time.Hour), cria("maq-a", time.Minute), cria("maq-a", time.Hour)
	if !(j1 < j2 && j2 < jOutra) {
		t.Fatalf("IDs crescentes: %d %d %d", j1, j2, jOutra)
	}

	// Cancelar um pendente; um não pendente não é cancelado.
	if ok, _ := st.CancelJob(ctx, jCancelar, "thyago"); !ok {
		t.Error("cancelar pendente")
	}
	if ok, _ := st.CancelJob(ctx, jCancelar, "thyago"); ok {
		t.Error("cancelar de novo")
	}

	// Expiração: o de 1 minuto vence daqui a 2 minutos.
	if n, err := st.ExpireJobs(ctx, agora.Add(2*time.Minute)); n != 1 || err != nil {
		t.Errorf("expirar: %d %v", n, err)
	}

	// Entrega: só os pendentes da máquina, em ordem, uma vez.
	env, err := st.DeliverJobs(ctx, "maq-a", agora.Add(3*time.Minute))
	if err != nil || len(env) != 2 || env[0].Payload != `{"id":`+itoa(j1)+`}` || env[1].Payload != `{"id":`+itoa(j2)+`}` {
		t.Fatalf("entrega: %+v %v", env, err)
	}
	if env, _ := st.DeliverJobs(ctx, "maq-a", agora.Add(4*time.Minute)); len(env) != 0 {
		t.Error("no máximo uma vez")
	}
	if ok, _ := st.CancelJob(ctx, j1, "x"); ok {
		t.Error("entregue não se cancela")
	}

	j, found, _ := st.JobForMachine(ctx, "maq-a", j1)
	if !found || j.State != "entregue" || j.DeliveredAt == nil || j.CreatedBy != "thyago" {
		t.Fatalf("job: %+v", j)
	}
	if _, found, _ := st.JobForMachine(ctx, "maq-b", j1); found {
		t.Error("job de outra máquina")
	}

	if err := st.SaveScan(ctx, "maq-a", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	if at, _ := st.ScanReceivedAt(ctx, "maq-a"); at == nil {
		t.Error("busca recebida")
	}
	if ok, _ := st.FinishJob(ctx, j1, "concluido", "busca nova recebida", agora.Add(5*time.Minute)); !ok {
		t.Error("concluir")
	}
	if ok, _ := st.FinishJob(ctx, j1, "falhou", "repetido", agora.Add(6*time.Minute)); ok {
		t.Error("resultado repetido não muda o estado final")
	}

	lista, _ := st.Jobs(ctx, "maq-a", 10)
	estados := map[int64]string{}
	for _, r := range lista {
		estados[r.ID] = r.State
	}
	if len(lista) != 4 || lista[0].ID != jCancelar || estados[j1] != "concluido" || estados[j2] != "entregue" ||
		estados[jVencido] != "expirado" || estados[jCancelar] != "cancelado" {
		t.Errorf("lista: %+v", lista)
	}
	if todas, _ := st.Jobs(ctx, "", 2); len(todas) != 2 || todas[0].ID != jCancelar {
		t.Errorf("limite e ordem: %+v", todas)
	}
}

func itoa(n int64) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestJobsMemoria(t *testing.T) { testarJobs(t, NewMemory()) }

func TestJobsPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarJobs(t, NewPostgres(pool))
}
