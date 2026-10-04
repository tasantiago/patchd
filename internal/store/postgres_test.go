package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// bancoDeTeste conecta no PATCHD_TEST_DATABASE_URL, num schema próprio e descartável.
// Sem a variável, o teste é pulado: go test ./... continua rodando sem banco.
func bancoDeTeste(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PATCHD_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PATCHD_TEST_DATABASE_URL não definido: teste com PostgreSQL pulado")
	}
	ctx := context.Background()

	admin, err := Connect(ctx, url, 10*time.Second)
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

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func silencioso() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestContratoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarContrato(t, NewPostgres(pool))
}

func TestMigrateIdempotenteEProtegido(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()

	v1 := fstest.MapFS{"m/0001_a.sql": {Data: []byte("CREATE TABLE a (x int);")}}
	if err := migrate(ctx, pool, silencioso(), v1, "m"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool, silencioso(), v1, "m"); err != nil {
		t.Errorf("rodar de novo não deveria fazer nada: %v", err)
	}

	editada := fstest.MapFS{"m/0001_a.sql": {Data: []byte("CREATE TABLE a (x bigint);")}}
	if err := migrate(ctx, pool, silencioso(), editada, "m"); err == nil || !strings.Contains(err.Error(), "alterada") {
		t.Errorf("migration editada depois de aplicada deveria ser recusada: %v", err)
	}

	v2 := fstest.MapFS{
		"m/0001_a.sql": {Data: []byte("CREATE TABLE a (x int);")},
		"m/0002_b.sql": {Data: []byte("CREATE TABLE b (x int); INSERT INTO b VALUES (1), (2);")},
	}
	if err := migrate(ctx, pool, silencioso(), v2, "m"); err != nil {
		t.Fatalf("migration nova com vários comandos: %v", err)
	}
	if err := migrate(ctx, pool, silencioso(), v1, "m"); err == nil || !strings.Contains(err.Error(), "mais antigo") {
		t.Errorf("binário mais antigo que o banco deveria ser recusado: %v", err)
	}

	quebrada := fstest.MapFS{
		"m/0001_a.sql": {Data: []byte("CREATE TABLE a (x int);")},
		"m/0002_b.sql": {Data: []byte("CREATE TABLE b (x int); INSERT INTO b VALUES (1), (2);")},
		"m/0003_c.sql": {Data: []byte("CREATE TABLE c (x int); SELECT erro_de_proposito;")},
	}
	if err := migrate(ctx, pool, silencioso(), quebrada, "m"); err == nil {
		t.Fatal("migration com erro deveria falhar")
	}
	var existe bool
	_ = pool.QueryRow(ctx, "SELECT to_regclass('c') IS NOT NULL").Scan(&existe)
	if existe {
		t.Error("migration que falhou no meio não pode deixar a tabela c: a transação inteira volta")
	}
}

func TestLoadMigrations(t *testing.T) {
	if _, err := loadMigrations(migrationFiles, "migrations"); err != nil {
		t.Errorf("as migrations do binário deveriam carregar: %v", err)
	}
	buraco := fstest.MapFS{"m/0001_a.sql": {Data: []byte("")}, "m/0003_c.sql": {Data: []byte("")}}
	if _, err := loadMigrations(buraco, "m"); err == nil || !strings.Contains(err.Error(), "sequência") {
		t.Errorf("versão pulada deveria ser recusada: %v", err)
	}
	nome := fstest.MapFS{"m/1_a.sql": {Data: []byte("")}}
	if _, err := loadMigrations(nome, "m"); err == nil || !strings.Contains(err.Error(), "padrão") {
		t.Errorf("nome fora do padrão deveria ser recusado: %v", err)
	}
}

func TestPostgresEnviosSimultaneosGravamUmaVez(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	// Dez envios iguais ao mesmo tempo (um agente que repete o POST depois de um prazo
	// vencido, por exemplo): a trava da linha da máquina garante uma única gravação.
	const n = 10
	resultados := make(chan bool, n)
	erros := make(chan error, n)
	for range n {
		go func() {
			gravou, err := st.SaveInventory(ctx, "corrida", inv("cccc", "1"))
			resultados <- gravou
			erros <- err
		}()
	}
	gravados := 0
	for range n {
		if <-resultados {
			gravados++
		}
		if err := <-erros; err != nil {
			t.Fatal(err)
		}
	}
	var linhas int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM inventory_reports WHERE machine_id = 'corrida'").Scan(&linhas)
	if gravados != 1 || linhas != 1 {
		t.Errorf("esperada uma gravação e uma linha, vieram %d gravações e %d linhas", gravados, linhas)
	}
}
