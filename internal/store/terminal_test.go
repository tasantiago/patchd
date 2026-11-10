package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoTerminal interface {
	contratoJobs
	OpenTerminalSession(ctx context.Context, s TerminalSession) (int64, error)
	ActiveTerminalSession(ctx context.Context, machineID, username string, now time.Time) (TerminalSession, bool, error)
	SetTerminalSessionExpiry(ctx context.Context, id int64, expires time.Time) error
	CreateTerminalCommand(ctx context.Context, c TerminalCommand) (int64, error)
	FinishTerminalCommand(ctx context.Context, jobID int64, exit *int, output string, truncated bool, note string, at time.Time) (bool, error)
	TerminalCommands(ctx context.Context, f TerminalFilter) ([]TerminalCommand, error)
	PruneTerminal(ctx context.Context, before time.Time) (int64, error)
}

// O registro do terminal: a sessão do admin na máquina (com o motivo e o prazo), o comando com
// o job, o resultado gravado uma vez só, o histórico filtrado e a retenção.
func testarTerminal(t *testing.T, st contratoTerminal) {
	ctx := context.Background()
	if _, err := st.SaveInventory(ctx, "term-a", protocol.InventoryReport{SchemaVersion: 1, Hash: "term-a"}); err != nil {
		t.Fatal(err)
	}
	agora := time.Now().UTC().Truncate(time.Second)
	if _, ok, err := st.ActiveTerminalSession(ctx, "term-a", "thyago", agora); ok || err != nil {
		t.Fatalf("sem sessão: %v %v", ok, err)
	}
	sid, err := st.OpenTerminalSession(ctx, TerminalSession{MachineID: "term-a", Username: "thyago", Reason: "chamado 123, conferir o spooler",
		Remote: "192.0.2.10", OpenedAt: agora, ExpiresAt: agora.Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if s, ok, _ := st.ActiveTerminalSession(ctx, "term-a", "thyago", agora.Add(time.Minute)); !ok || s.ID != sid || s.Reason != "chamado 123, conferir o spooler" {
		t.Fatalf("sessão: %+v %v", s, ok)
	}
	if _, ok, _ := st.ActiveTerminalSession(ctx, "term-a", "outro", agora); ok {
		t.Error("a sessão é de quem abriu")
	}
	if _, ok, _ := st.ActiveTerminalSession(ctx, "term-a", "thyago", agora.Add(16*time.Minute)); ok {
		t.Error("a sessão venceu")
	}
	jid, _ := st.NextJobID(ctx)
	if err := st.CreateJob(ctx, NewJob{ID: jid, MachineID: "term-a", Type: "comando", Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
		CreatedBy: "thyago", NotAfter: agora.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	cid, err := st.CreateTerminalCommand(ctx, TerminalCommand{SessionID: sid, MachineID: "term-a", Username: "thyago", Remote: "192.0.2.10",
		Shell: "powershell", Command: "Get-Service Spooler", TimeoutSec: 60, JobID: jid, CreatedAt: agora})
	if err != nil {
		t.Fatal(err)
	}
	l, err := st.TerminalCommands(ctx, TerminalFilter{MachineID: "term-a", Limit: 10})
	if err != nil || len(l) != 1 || l[0].ID != cid || l[0].FinishedAt != nil || l[0].JobState != "pendente" || l[0].Reason == "" || l[0].JobID != jid {
		t.Fatalf("antes do resultado: %+v %v", l, err)
	}
	zero := 0
	if ok, err := st.FinishTerminalCommand(ctx, jid, &zero, "Running  Spooler", false, "código de saída 0 em 1s", agora.Add(2*time.Second)); !ok || err != nil {
		t.Fatalf("resultado: %v %v", ok, err)
	}
	um := 1
	if ok, _ := st.FinishTerminalCommand(ctx, jid, &um, "outra", false, "x", agora); ok {
		t.Error("o resultado foi trocado")
	}
	l, _ = st.TerminalCommands(ctx, TerminalFilter{Username: "thyago", Limit: 10})
	if len(l) != 1 || l[0].ExitCode == nil || *l[0].ExitCode != 0 || l[0].Output != "Running  Spooler" || l[0].Command != "Get-Service Spooler" {
		t.Fatalf("depois: %+v", l)
	}
	if l, _ := st.TerminalCommands(ctx, TerminalFilter{Username: "ninguem", Limit: 10}); len(l) != 0 {
		t.Error("filtro por administrador")
	}
	if l, _ := st.TerminalCommands(ctx, TerminalFilter{MachineID: "term-a", Limit: 10, Offset: 1}); len(l) != 0 {
		t.Error("página seguinte")
	}
	if err := st.SetTerminalSessionExpiry(ctx, sid, agora); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ActiveTerminalSession(ctx, "term-a", "thyago", agora.Add(time.Second)); ok {
		t.Error("a sessão fechada continua")
	}
	if n, err := st.PruneTerminal(ctx, agora.Add(-time.Hour)); n != 0 || err != nil {
		t.Errorf("retenção cedo: %d %v", n, err)
	}
	if n, err := st.PruneTerminal(ctx, agora.Add(time.Hour)); n != 1 || err != nil {
		t.Errorf("retenção: %d %v", n, err)
	}
	if l, _ := st.TerminalCommands(ctx, TerminalFilter{Limit: 10}); len(l) != 0 {
		t.Error("ficou depois da retenção")
	}
}

func TestTerminalMemoria(t *testing.T) { testarTerminal(t, NewMemory()) }

func TestTerminalPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarTerminal(t, NewPostgres(pool))
}
