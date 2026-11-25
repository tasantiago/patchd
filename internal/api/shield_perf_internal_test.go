package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
)

// TestShieldDezMil mede o avaliador com 10.000 máquinas (só com PATCHD_SHIELD_PERF=1: leva
// alguns segundos). A primeira passada grava tudo; a segunda, sem mudança, quase nada.
func TestShieldDezMil(t *testing.T) {
	if os.Getenv("PATCHD_SHIELD_PERF") != "1" {
		t.Skip("PATCHD_SHIELD_PERF=1 para medir")
	}
	st, pool := shieldPG(t)
	ctx := context.Background()
	agora := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	const n = 10000
	if _, err := pool.Exec(ctx, `INSERT INTO machines (id, ring, last_seen_at, state)
		SELECT 'm-' || g, g % 10, $1, jsonb_build_object('users', jsonb_build_array(jsonb_build_object('account', 'EXEMPLO\u' || g, 'session', 'console', 'active', true)))
		FROM generate_series(1, $2) g`, agora, n); err != nil {
		t.Fatal(err)
	}
	inv := agora.Add(-time.Hour)
	cs := &complianceShield{}
	for i := 1; i <= n; i++ {
		cs.status = append(cs.status, protocol.ComplianceStatus{MachineID: fmt.Sprintf("m-%d", i), State: "em_dia", InventoryCollectedAt: &inv, Catalog: "x", Pending: i % 7})
	}
	pol := shield.Default()
	pol.Enabled = true
	if _, err := st.SaveShieldPolicy(ctx, pol, "", "admin"); err != nil {
		t.Fatal(err)
	}
	a := &api{store: st, compliance: cs, logger: slog.New(slog.DiscardHandler), now: func() time.Time { return agora }}
	for i, quando := range []time.Time{agora, agora.Add(5 * time.Minute), agora.Add(20 * time.Hour)} {
		start := time.Now()
		if err := a.shieldRun(ctx, st, quando); err != nil {
			t.Fatal(err)
		}
		t.Logf("passada %d: %v", i+1, time.Since(start))
	}
}
