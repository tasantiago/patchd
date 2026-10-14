package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoSessoesEContagem interface {
	contratoJobs
	CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error
	PanelSessions(ctx context.Context) ([]PanelSessionRow, error)
	DeleteUserSessions(ctx context.Context, username string) (int64, error)
	JobCounts(ctx context.Context, since time.Time) ([]JobCount, error)
	JobsFinishedSince(ctx context.Context, states []string, since time.Time) (int, error)
}

// Aula 15.4: as sessões abertas (listar e encerrar as de um usuário) e a contagem dos jobs
// no banco.
func testarSessoesEContagem(t *testing.T, st contratoSessoesEContagem) {
	ctx := context.Background()
	agora := time.Now().UTC().Truncate(time.Microsecond)
	for i, s := range []panel.Session{
		{Username: "ana", Role: panel.RoleAdmin, Source: panel.SourceAD},
		{Username: "ana", Role: panel.RoleAdmin, Source: panel.SourceLocal},
		{Username: "bia", Role: panel.RoleRead, Source: panel.SourceOIDC},
	} {
		s.CreatedAt, s.ExpiresAt, s.LastSeenAt = agora, agora.Add(time.Hour), agora.Add(time.Duration(i)*time.Minute)
		if err := st.CreatePanelSession(ctx, []byte{byte(i), 1, 2}, s); err != nil {
			t.Fatal(err)
		}
	}
	l, err := st.PanelSessions(ctx)
	if err != nil || len(l) != 3 || l[0].Username != "bia" || l[0].Source != panel.SourceOIDC || len(l[0].Hash) != 3 || !l[2].LastSeenAt.Equal(agora) {
		t.Fatalf("sessões: %+v %v", l, err)
	}
	if n, err := st.DeleteUserSessions(ctx, "ana"); n != 2 || err != nil {
		t.Errorf("encerrar as da ana: %d %v", n, err)
	}
	if l, _ := st.PanelSessions(ctx); len(l) != 1 || l[0].Username != "bia" {
		t.Errorf("sobrou: %+v", l)
	}

	// Contagem.
	if _, err := st.SaveInventory(ctx, "cont-a", protocol.InventoryReport{SchemaVersion: 1, Hash: "cont-a"}); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"rescan", "rescan", "update"} {
		id, _ := st.NextJobID(ctx)
		if err := st.CreateJob(ctx, NewJob{ID: id, MachineID: "cont-a", Type: typ, Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "t", NotAfter: agora.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if typ == "update" {
			st.FinishJob(ctx, id, "falhou", "x", agora)
		}
	}
	c, err := st.JobCounts(ctx, agora.Add(-time.Hour))
	if err != nil || len(c) != 2 || c[0] != (JobCount{"rescan", "pendente", 2}) || c[1] != (JobCount{"update", "falhou", 1}) {
		t.Errorf("contagem: %+v %v", c, err)
	}
	if c, _ := st.JobCounts(ctx, agora.Add(time.Hour)); len(c) != 0 {
		t.Errorf("depois: %+v", c)
	}
	if n, err := st.JobsFinishedSince(ctx, []string{"falhou", "rejeitado"}, agora.Add(-time.Minute)); n != 1 || err != nil {
		t.Errorf("sem sucesso: %d %v", n, err)
	}
	if n, _ := st.JobsFinishedSince(ctx, []string{"falhou"}, agora.Add(time.Minute)); n != 0 {
		t.Errorf("fora da janela: %d", n)
	}
}

func TestSessoesEContagemMemoria(t *testing.T) { testarSessoesEContagem(t, NewMemory()) }

func TestSessoesEContagemPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarSessoesEContagem(t, NewPostgres(pool))
}
