package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/catalog/osv"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// parqueDeTeste: N máquinas Ubuntu 26.04 com ~800 pacotes (200 fontes) num schema descartável
// do PATCHD_TEST_DATABASE_URL, com os USN do testdata do OSV e a sincronização registrada.
func parqueDeTeste(b testing.TB, n int) *store.Postgres {
	b.Helper()
	url := os.Getenv("PATCHD_TEST_DATABASE_URL")
	if url == "" {
		b.Skip("PATCHD_TEST_DATABASE_URL não definido")
	}
	ctx := context.Background()
	admin, err := store.Connect(ctx, url, 10*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	r := make([]byte, 6)
	_, _ = rand.Read(r)
	schema := "bench_" + hex.EncodeToString(r)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := store.Migrate(ctx, pool, logger); err != nil {
		b.Fatal(err)
	}
	pg := store.NewPostgres(pool)
	for _, f := range []string{"USN-8861-1", "USN-8862-1", "USN-8863-1"} {
		fh, err := os.Open("../../internal/catalog/osv/testdata/" + f + ".json")
		if err != nil {
			b.Fatal(err)
		}
		rec, err := osv.Parse(fh)
		fh.Close()
		if err != nil {
			b.Fatal(err)
		}
		if err := pg.SaveUbuntuUSN(ctx, rec, "2026-10-01T00:00:00Z"); err != nil {
			b.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, src := range catalogSourceNames {
		_ = pg.RecordCatalogRun(ctx, src, "teste", now.Add(-time.Minute), now, true, "")
	}
	sw := make([]protocol.Software, 0, 800)
	for i := range 800 {
		src := fmt.Sprintf("fonte-%d", i%200)
		if i == 0 {
			src = "openssl"
		}
		sw = append(sw, protocol.Software{Name: fmt.Sprintf("pacote-%d", i), Version: "1.0-1ubuntu1", SourcePackage: src, SourcePackageVersion: "1.0-1ubuntu1"})
	}
	for i := range n {
		id := fmt.Sprintf("bench-%04d", i)
		inv := protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:" + fmt.Sprintf("%064d", i), CollectedAt: now,
			OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04", Hostname: id, Kernel: "7.0.0-34-generic"}, Software: sw}
		if _, err := pg.SaveInventory(ctx, id, inv); err != nil {
			b.Fatal(err)
		}
	}
	return pg
}

// BenchmarkParque: o Fleet de 300 máquinas (o que Parque, Visão geral e Patch pedem), com o
// cache vazio (a primeira página depois da partida, ou tudo mudou) e com o cache quente.
//
//	PATCHD_TEST_DATABASE_URL=... go test ./cmd/server -run x -bench Parque -benchtime 5x
func BenchmarkParque(b *testing.B) {
	pg := parqueDeTeste(b, 300)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	fleet := func(s *complianceService) {
		f, err := s.Fleet(ctx)
		if err != nil || len(f.Machines) != 300 {
			b.Fatal(len(f.Machines), err)
		}
	}
	b.Run("frio", func(b *testing.B) {
		for range b.N {
			fleet(newComplianceService(pg, logger))
		}
	})
	b.Run("quente", func(b *testing.B) {
		s := newComplianceService(pg, logger)
		fleet(s)
		b.ResetTimer()
		for range b.N {
			fleet(s)
		}
	})
}

// TestTempoDasPaginas mede as páginas do painel com o parque de 300 máquinas (frio e quente).
// Só roda com PATCHD_TEST_PAGINAS=1 (é medida, não conferência).
func TestTempoDasPaginas(t *testing.T) {
	if os.Getenv("PATCHD_TEST_PAGINAS") == "" {
		t.Skip("PATCHD_TEST_PAGINAS não definido")
	}
	pg := parqueDeTeste(t, 300)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agora := time.Now().UTC()
	if err := pg.CreatePanelSession(ctx, identity.Hash(panel.SessionPrefix+"sessao-admin-de-medida"), panel.Session{Username: "admin", Role: panel.RoleAdmin,
		Source: panel.SourceLocal, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	cs := newComplianceService(pg, logger)
	h := api.New(pg, logger, api.WithCompliance(cs))
	get := func(path string) time.Duration {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: panel.SessionPrefix + "sessao-admin-de-medida"})
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, req)
		d := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rec.Code)
		}
		return d
	}
	for _, p := range []string{"/painel/", "/painel/parque", "/painel/inventario", "/painel/patch?aba=geral", "/painel/patch", "/painel/patch?aba=terceiros",
		"/painel/maquinas/bench-0007", "/painel/maquinas/bench-0007?aba=hardware", "/painel/maquinas/bench-0007?aba=software",
		"/painel/maquinas/bench-0007?aba=pendencias", "/painel/jobs", "/painel/alertas", "/painel/configuracao", "/painel/campanhas"} {
		frio := get(p)
		quente := get(p)
		t.Logf("%-48s frio %6d ms   quente %6d ms", p, frio.Milliseconds(), quente.Milliseconds())
	}
}
