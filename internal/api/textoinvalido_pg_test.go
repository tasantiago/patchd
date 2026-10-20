package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// postgresDeTeste: um schema novo no banco de teste, com as migrações (pulado sem banco).
func postgresDeTeste(t *testing.T) *store.Postgres {
	t.Helper()
	dsn := os.Getenv("PATCHD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PATCHD_TEST_DATABASE_URL não definido: teste com PostgreSQL pulado")
	}
	ctx := context.Background()
	admin, err := store.Connect(ctx, dsn, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "teste_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return store.NewPostgres(pool)
}

// TestRotasComTextoInvalido (Aula 20.4b, F8): toda rota do api.go, com sessão de admin e de
// leitura, com NUL e UTF-8 inválido no caminho, na URL e no formulário, contra o PostgreSQL
// de verdade (a memória aceita qualquer texto e esconderia o erro). Nenhuma resposta 5xx,
// nenhum "falha interna" e nenhuma "auditoria não gravada" no log.
func TestRotasComTextoInvalido(t *testing.T) {
	st := postgresDeTeste(t)
	ctx := context.Background()
	agora := time.Now().UTC()
	for cookie, role := range map[string]string{sessaoAdmin: panel.RoleAdmin, sessaoTeste: panel.RoleRead} {
		if err := st.CreatePanelSession(ctx, identity.Hash(cookie), panel.Session{Username: "u-" + role, Role: role, Source: panel.SourceLocal,
			CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
			t.Fatal(err)
		}
	}
	log := &syncBuffer{}
	_, priv, _ := jobs.GenerateKey()
	h := api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithJobKey(priv), api.WithCompliance(&complianceFalso{}))

	ruins := []string{"%00", "%C3%28"}
	campos := []string{"id", "fix", "app", "nota", "motivo", "usuario", "senha", "nome", "q", "status", "maquina", "programa", "version", "tipo", "grupos"}
	n := 0
	for _, r := range lerRotas(t) {
		if r.Caminho == "/painel/sair" {
			continue // encerraria as sessões do teste
		}
		base := caminhoDeTeste(r.Caminho)
		for _, ruim := range ruins {
			// O valor ruim vem primeiro (o FormValue pega o primeiro).
			corpo := strings.Join(campos, "="+ruim+"&") + "=" + ruim
			for _, caminho := range []string{base + ruim, base + "?" + strings.Join(campos, "="+ruim+"&") + "=" + ruim, base} {
				for _, cookie := range []string{sessaoAdmin, sessaoTeste} {
					req := httptest.NewRequest(r.Metodo, "http://patchd.exemplo"+caminho, strings.NewReader(corpo))
					if r.Metodo == "POST" {
						req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
					w := httptest.NewRecorder()
					h.ServeHTTP(w, req)
					n++
					if w.Code >= 500 {
						t.Errorf("%s %s: %d %s", r.Metodo, caminho, w.Code, strings.TrimSpace(w.Body.String()))
					}
				}
			}
		}
	}
	for _, ruim := range regexp.MustCompile(`(?m)^.*(falha interna|auditoria não gravada).*$`).FindAllString(log.String(), 5) {
		t.Errorf("log: %s", ruim)
	}
	if n < 500 {
		t.Errorf("só %d pedidos: a leitura do api.go quebrou?", n)
	}
}
