package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// As migrations vão dentro do binário: a imagem scratch não tem outros arquivos.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID identifica o lock consultivo das migrations. Duas instâncias do
// servidor subindo juntas não aplicam a mesma migration duas vezes: a segunda espera.
const migrationLockID int64 = 7_243_042_001

// migrationName: "0001_inicial.sql" = versão 1, nome "inicial".
var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

// loadMigrations lê os arquivos, exige nomes no formato e versões contíguas a partir de 1.
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var list []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("arquivo fora do padrão NNNN_nome.sql em migrations: %s", e.Name())
		}
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		v, _ := strconv.Atoi(m[1])
		sum := sha256.Sum256(data)
		list = append(list, migration{version: v, name: m[2], sql: string(data), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })
	for i, m := range list {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations fora de sequência: esperada a versão %d, encontrada %d (%s)", i+1, m.version, m.name)
		}
	}
	return list, nil
}

// Migrate aplica as migrations pendentes, cada uma na sua transação, e registra cada
// uma em schema_migrations com o SHA-256 do arquivo. Recusa seguir se uma migration já
// aplicada foi editada ou se o banco está numa versão que este binário não conhece.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return migrate(ctx, pool, logger, migrationFiles, "migrations")
}

func migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, fsys fs.FS, dir string) error {
	list, err := loadMigrations(fsys, dir)
	if err != nil {
		return err
	}

	// O lock consultivo é da sessão: tudo precisa rodar na mesma conexão.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrations: conexão: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migrations: lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		name       text        NOT NULL,
		checksum   text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrations: tabela de controle: %w", err)
	}

	applied := map[int]string{}
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("migrations: leitura do controle: %w", err)
	}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			rows.Close()
			return err
		}
		applied[v] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	known := map[int]migration{}
	for _, m := range list {
		known[m.version] = m
	}
	for v, sum := range applied {
		m, ok := known[v]
		if !ok {
			return fmt.Errorf("o banco tem a migration %d, que este binário não conhece: o servidor é mais antigo que o banco", v)
		}
		if m.checksum != sum {
			return fmt.Errorf("a migration %04d_%s foi alterada depois de aplicada (SHA-256 diferente): crie uma migration nova em vez de editar", v, m.name)
		}
	}

	for _, m := range list {
		if _, done := applied[m.version]; done {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// Sem argumentos, o pgx usa o protocolo simples: o arquivo pode ter vários comandos.
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
			m.version, m.name, m.checksum); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %04d_%s: registro: %w", m.version, m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migration %04d_%s: commit: %w", m.version, m.name, err)
		}
		logger.Info("migration aplicada", "migration", m.version, "name", m.name)
	}
	logger.Info("banco na versão atual", "schema_version", len(list))
	return nil
}
