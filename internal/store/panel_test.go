package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
)

// contratoPainel: usuários e sessões, iguais na memória e no PostgreSQL.
type contratoPainel interface {
	CreatePanelUser(ctx context.Context, u panel.User) error
	PanelUser(ctx context.Context, name string) (panel.User, bool, error)
	PanelUsers(ctx context.Context) ([]panel.User, error)
	SetPanelPassword(ctx context.Context, name, hash string) (bool, error)
	DeletePanelUser(ctx context.Context, name string) (bool, error)
	CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error
	PanelSession(ctx context.Context, tokenHash []byte) (panel.Session, bool, error)
	TouchPanelSession(ctx context.Context, tokenHash []byte, at time.Time) error
	DeletePanelSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredPanelSessions(ctx context.Context, now time.Time) (int64, error)
}

func testarPainel(t *testing.T, st contratoPainel) {
	ctx := context.Background()

	if _, found, err := st.PanelUser(ctx, "ninguem"); found || err != nil {
		t.Fatalf("usuário inexistente: %t %v", found, err)
	}
	if err := st.CreatePanelUser(ctx, panel.User{Name: "thyago", Role: panel.RoleAdmin, PasswordHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePanelUser(ctx, panel.User{Name: "consulta", Role: panel.RoleRead, PasswordHash: "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePanelUser(ctx, panel.User{Name: "thyago", Role: panel.RoleRead, PasswordHash: "h3"}); !errors.Is(err, panel.ErrUserExists) {
		t.Errorf("nome repetido: %v", err)
	}
	u, found, err := st.PanelUser(ctx, "thyago")
	if err != nil || !found || u.Role != panel.RoleAdmin || u.PasswordHash != "h1" || u.CreatedAt.IsZero() {
		t.Fatalf("usuário: %+v %t %v", u, found, err)
	}
	lista, err := st.PanelUsers(ctx)
	if err != nil || len(lista) != 2 || lista[0].Name != "consulta" || lista[1].PasswordHash != "" {
		t.Fatalf("lista em ordem de nome e sem hash: %+v %v", lista, err)
	}

	t0 := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	sessao := func(user, source string) panel.Session {
		return panel.Session{Username: user, Role: panel.RoleAdmin, Source: source, CreatedAt: t0, ExpiresAt: t0.Add(panel.SessionMaxAge), LastSeenAt: t0}
	}
	hLocal, hAD, hOutra := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	for h, s := range map[*[]byte]panel.Session{&hLocal: sessao("thyago", panel.SourceLocal), &hAD: sessao("thyago", panel.SourceAD), &hOutra: sessao("consulta", panel.SourceLocal)} {
		if err := st.CreatePanelSession(ctx, *h, s); err != nil {
			t.Fatal(err)
		}
	}
	s, found, err := st.PanelSession(ctx, hLocal)
	if err != nil || !found || s != sessao("thyago", panel.SourceLocal) {
		t.Fatalf("sessão: %+v %t %v", s, found, err)
	}
	if err := st.TouchPanelSession(ctx, hLocal, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.PanelSession(ctx, hLocal); !s.LastSeenAt.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("uso registrado: %+v", s)
	}

	// Trocar a senha encerra as sessões locais do usuário, e só elas.
	if ok, err := st.SetPanelPassword(ctx, "thyago", "h9"); !ok || err != nil {
		t.Fatalf("troca de senha: %t %v", ok, err)
	}
	if u, _, _ := st.PanelUser(ctx, "thyago"); u.PasswordHash != "h9" {
		t.Error("hash novo")
	}
	if _, found, _ := st.PanelSession(ctx, hLocal); found {
		t.Error("a sessão local deveria ter sido encerrada")
	}
	if _, found, _ := st.PanelSession(ctx, hAD); !found {
		t.Error("a sessão do AD não depende da senha local")
	}
	if ok, _ := st.SetPanelPassword(ctx, "ninguem", "x"); ok {
		t.Error("usuário inexistente")
	}

	// Vencidas: a do AD por inatividade (last_seen t0), a outra continua com uso recente.
	if err := st.TouchPanelSession(ctx, hOutra, t0.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := st.DeleteExpiredPanelSessions(ctx, t0.Add(45*time.Minute))
	if err != nil || n != 1 {
		t.Errorf("vencidas apagadas: %d %v", n, err)
	}
	if _, found, _ := st.PanelSession(ctx, hOutra); !found {
		t.Error("a sessão em uso continua")
	}
	if n, _ := st.DeleteExpiredPanelSessions(ctx, t0.Add(panel.SessionMaxAge)); n != 1 {
		t.Errorf("prazo absoluto: %d", n)
	}

	if err := st.CreatePanelSession(ctx, hOutra, sessao("consulta", panel.SourceLocal)); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeletePanelUser(ctx, "consulta"); !ok || err != nil {
		t.Fatalf("remover: %t %v", ok, err)
	}
	if _, found, _ := st.PanelSession(ctx, hOutra); found {
		t.Error("remover o usuário encerra as sessões dele")
	}
	if ok, _ := st.DeletePanelUser(ctx, "consulta"); ok {
		t.Error("remover de novo")
	}
	if err := st.DeletePanelSession(ctx, hAD); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.PanelSession(ctx, hAD); found {
		t.Error("sessão encerrada")
	}
}

func TestPainelMemoria(t *testing.T) { testarPainel(t, NewMemory()) }

func TestPainelPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	testarPainel(t, st)
	// As restrições do banco recusam perfil e origem desconhecidos.
	if err := st.CreatePanelUser(context.Background(), panel.User{Name: "x1", Role: "root", PasswordHash: "h"}); err == nil {
		t.Error("perfil fora da lista")
	}
	s := panel.Session{Username: "x1", Role: panel.RoleRead, Source: "ldap", CreatedAt: time.Now(), ExpiresAt: time.Now(), LastSeenAt: time.Now()}
	if err := st.CreatePanelSession(context.Background(), []byte("h"), s); err == nil {
		t.Error("origem fora da lista")
	}
}
