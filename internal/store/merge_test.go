package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mergeTestStore interface {
	contratoAposentadoria
	contratoGuardaBIOS
	MergeMachines(context.Context, string, string, string, AuditEvent) error
	MachineMergeMembers(context.Context, string) ([]MachineMerge, error)
	MachineReportHistory(context.Context, string, string, int64) ([]MachineReportRecord, error)
	MachineReportByID(context.Context, string, string, int64) (json.RawMessage, error)
	LatestInventory(context.Context, string) (protocol.InventoryReport, bool, error)
	NextJobID(context.Context) (int64, error)
	CreateJob(context.Context, NewJob) error
	Jobs(context.Context, string, int) ([]JobRow, error)
	DeliverJobs(context.Context, string, time.Time) ([]protocol.SignedJob, error)
	FinishJob(context.Context, int64, string, string, time.Time) (bool, error)
	AuditEvents(context.Context, AuditFilter) ([]AuditEvent, error)
}

func testMergeMachines(t *testing.T, st mergeTestStore) {
	ctx := context.Background()
	now := time.Now().UTC()
	_, tokenHash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, tokenHash, "merge test", now.Add(time.Hour), 3); err != nil {
		t.Fatal(err)
	}
	hashes := map[string][]byte{}
	for _, id := range []string{"merge-old", "merge-current", "merge-next"} {
		nm, _ := novaMaquina()
		nm.ID = id
		hashes[id] = nm.CredentialHash
		if _, err := st.Enroll(ctx, tokenHash, nm); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "history-" + id, CollectedAt: now, OS: protocol.OSInfo{Hostname: id}}); err != nil {
			t.Fatal(err)
		}
	}
	key, err := machinebox.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := machinebox.Public(key)
	password, _ := biospw.Generate()
	envelope, err := biospw.Seal(pub, "merge-old", password)
	if err != nil {
		t.Fatal(err)
	}
	guard, created, err := st.CreateBIOSPassword(ctx, BIOSPassword{MachineID: "merge-old", Escrow: envelope, EscrowKeyID: machinebox.KeyID(pub), CreatedAt: now, CreatedBy: "admin"})
	if err != nil || !created {
		t.Fatal("guard creation", err)
	}
	if _, err := st.AttachBIOSJob(ctx, guard.ID, 9001, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SettleBIOSPassword(ctx, guard.ID, 9001, BIOSActive, "gravação comprovada", now); err != nil {
		t.Fatal(err)
	}
	makeJob := func(id string) int64 {
		t.Helper()
		n, err := st.NextJobID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateJob(ctx, NewJob{ID: n, MachineID: id, Type: "rescan", Signed: protocol.SignedJob{Payload: "original-signed-payload", Signature: "sig"}, CreatedBy: "admin", NotAfter: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	busy := makeJob("merge-old")
	if _, err := st.DeliverJobs(ctx, "merge-old", now); err != nil {
		t.Fatal(err)
	}
	event := AuditEvent{At: now, Username: "admin", Source: "local"}
	if err := st.MergeMachines(ctx, "merge-old", "merge-current", "Agente reinstalado no mesmo equipamento", event); !errors.Is(err, ErrMergeBusy) {
		t.Fatalf("in-flight work: %v", err)
	}
	if _, found, err := st.MachineByCredential(ctx, hashes["merge-old"]); !found || err != nil {
		t.Fatal("busy merge changed credential")
	}
	if _, err := st.FinishJob(ctx, busy, "concluido", "done", now); err != nil {
		t.Fatal(err)
	}
	makeJob("merge-old")
	if err := st.MergeMachines(ctx, "merge-old", "merge-current", "Agente reinstalado no mesmo equipamento", event); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.MachineByCredential(ctx, hashes["merge-old"]); found || err != nil {
		t.Fatal("old credential still works", err)
	}
	if id, found, err := st.MachineByCredential(ctx, hashes["merge-current"]); !found || err != nil || id != "merge-current" {
		t.Fatal("current credential changed", err)
	}
	if restored, err := st.RestoreMachine(ctx, "merge-old"); restored || err != nil {
		t.Fatal("revived incorporated identity", err)
	}
	preserved, found, err := st.BIOSPasswordByID(ctx, guard.ID)
	if err != nil || !found || preserved.MachineID != "merge-old" || !bytes.Equal(envelope, preserved.Escrow) {
		t.Fatal("encrypted history changed", err)
	}
	opened, err := biospw.Open(key, preserved.MachineID, preserved.Escrow)
	if err != nil || opened != password {
		t.Fatal("lost BIOS password", err)
	}
	if _, err := biospw.Open(key, "merge-current", preserved.Escrow); !errors.Is(err, biospw.ErrOtherMachine) {
		t.Fatal("original binding was lost")
	}
	reports, err := st.MachineReportHistory(ctx, "merge-old", "inventario", 0)
	if err != nil || len(reports) != 1 {
		t.Fatal("lost old inventory", err)
	}
	report, err := st.MachineReportByID(ctx, "merge-old", "inventario", reports[0].ID)
	if err != nil || !bytes.Contains(report, []byte("merge-old")) {
		t.Fatal("old report unreadable", err)
	}
	inv, found, err := st.LatestInventory(ctx, "merge-current")
	if err != nil || !found || inv.OS.Hostname != "merge-current" {
		t.Fatal("overwrote current inventory", err)
	}
	jobRows, err := st.Jobs(ctx, "merge-old", 10)
	if err != nil || len(jobRows) != 2 || jobRows[0].State != "cancelado" {
		t.Fatal("job history lost", err)
	}
	audit, err := st.AuditEvents(ctx, AuditFilter{Action: "maquina.mesclar"})
	if err != nil || len(audit) != 1 || audit[0].Target != "merge-current" {
		t.Fatal("merge audit missing", err)
	}
	if err := st.MergeMachines(ctx, "merge-current", "merge-old", "Tentativa de ciclo de identidades", event); !errors.Is(err, ErrMergeInvalid) {
		t.Fatal("accepted a cycle", err)
	}
	if err := st.MergeMachines(ctx, "merge-current", "merge-next", "Nova reinstalação no mesmo equipamento", event); err != nil {
		t.Fatal(err)
	}
	members, err := st.MachineMergeMembers(ctx, "merge-old")
	if err != nil || len(members) != 3 {
		t.Fatal("lost nested history", err)
	}
	for _, m := range members {
		if m.TargetID != "merge-next" {
			t.Fatal("group not flattened")
		}
	}
}
func TestMergeMachinesMemory(t *testing.T) { testMergeMachines(t, NewMemory()) }
func TestMergeMachinesPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testMergeMachines(t, NewPostgres(pool))
}

func testConcurrentMachineMerge(t *testing.T, st mergeTestStore) {
	ctx := context.Background()
	for _, id := range []string{"concurrent-source", "concurrent-a", "concurrent-b"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for _, target := range []string{"concurrent-a", "concurrent-b"} {
		wg.Go(func() {
			err := st.MergeMachines(ctx, "concurrent-source", target, "Registro duplicado confirmado pelo administrador", AuditEvent{At: time.Now().UTC(), Username: "admin"})
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrMergeInvalid) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent merge winners: %d", wins.Load())
	}
	group, err := st.MachineMergeMembers(ctx, "concurrent-source")
	if err != nil || len(group) != 2 {
		t.Fatal("invalid merged group", err)
	}
}
func TestConcurrentMachineMergeMemory(t *testing.T) { testConcurrentMachineMerge(t, NewMemory()) }
func TestConcurrentMachineMergePostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testConcurrentMachineMerge(t, NewPostgres(pool))
}
func TestMachineMergeAuditFailureRollsBackPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	for _, id := range []string{"audit-source", "audit-target"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_merge_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='maquina.mesclar' THEN RAISE EXCEPTION 'test audit failure'; END IF; RETURN NEW; END; $$;
 CREATE TRIGGER reject_merge_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_merge_audit();`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MergeMachines(ctx, "audit-source", "audit-target", "Registro duplicado confirmado pelo administrador", AuditEvent{At: time.Now().UTC(), Username: "admin"}); err == nil {
		t.Fatal("merge succeeded without audit")
	}
	members, err := st.MachineMergeMembers(ctx, "audit-target")
	if err != nil || len(members) != 1 {
		t.Fatal("partial membership survived audit failure", err)
	}
	retired, err := st.RetiredMachines(ctx)
	if err != nil || len(retired) != 0 {
		t.Fatal("source retired despite rollback", err)
	}
}
