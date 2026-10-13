package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestRetencao(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	// Histórico: a com 3 inventários e 2 buscas, b com 1 de cada. Tudo com 100 dias.
	for i, h := range []string{"a1", "a2", "a3"} {
		if _, err := st.SaveInventory(ctx, "a", protocol.InventoryReport{SchemaVersion: 1, Hash: h}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := st.SaveInventory(ctx, "b", protocol.InventoryReport{SchemaVersion: 1, Hash: "b1"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "a", "b"} {
		if err := st.SaveScan(ctx, id, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion}); err != nil {
			t.Fatal(err)
		}
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`UPDATE inventory_reports SET received_at = now() - interval '100 days'`)
	mustExec(`UPDATE scan_reports SET received_at = now() - interval '100 days'`)

	// Campanhas: encerrada velha (sai depois dos jobs), em andamento velha e encerrada nova (ficam).
	campanha := func(state, idade string) int64 {
		t.Helper()
		var id int64
		err := pool.QueryRow(ctx, `
			INSERT INTO campaigns (name, type, rings, use_window, observe_sec, ttl_sec, state, created_by, updated_at)
			VALUES ('c', 'rescan', '{0}', false, 0, 3600, $1, 'teste', now() - $2::interval) RETURNING id`, state, idade).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	velha, andamento, nova := campanha("concluida", "500 days"), campanha("em_andamento", "500 days"), campanha("cancelada", "10 days")
	job := func(state, idade string, camp any) {
		t.Helper()
		mustExec(`
			INSERT INTO jobs (machine_id, type, state, payload, signature, created_by, created_at, not_after, finished_at, campaign_id)
			VALUES ('a', 'rescan', $1, '{}', 'sig', 'teste', now() - $2::interval, now() - $2::interval + interval '1 hour',
			        CASE WHEN $1 IN ('pendente', 'entregue') THEN NULL ELSE now() - $2::interval END, $3)`, state, idade, camp)
	}
	job("concluido", "500 days", nil)       // sai
	job("expirado", "500 days", nil)        // sai
	job("concluido", "10 days", nil)        // fica: recente
	job("pendente", "500 days", nil)        // fica: não encerrado
	job("concluido", "500 days", velha)     // sai: campanha encerrada
	job("concluido", "500 days", andamento) // fica: campanha em andamento
	job("falhou", "500 days", nova)         // fica: campanha recente, mas o job é velho
	_ = nova

	r := Retention{History: 90 * 24 * time.Hour, Jobs: 400 * 24 * time.Hour}
	quer := PruneResult{Inventories: 2, Scans: 1, Jobs: 4, Campaigns: 1}
	// Simulação: as contagens certas e nada apagado.
	got, err := st.Prune(ctx, r, time.Now(), true)
	if err != nil || got != quer {
		t.Fatalf("simulação: %+v %v (quer %+v)", got, err, quer)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM inventory_reports) + (SELECT count(*) FROM jobs) + (SELECT count(*) FROM campaigns)`).Scan(&n)
	if n != 4+7+3 {
		t.Fatalf("a simulação apagou: sobraram %d linhas", n)
	}
	// De verdade.
	if got, err := st.Prune(ctx, r, time.Now(), false); err != nil || got != quer {
		t.Fatalf("aplicação: %+v %v", got, err)
	}
	for _, id := range []string{"a", "b"} {
		if _, found, err := st.LatestInventory(ctx, id); !found || err != nil {
			t.Errorf("o inventário atual de %s precisa ficar: %v %v", id, found, err)
		}
		if _, found, err := st.LatestScan(ctx, id); !found || err != nil {
			t.Errorf("a busca atual de %s precisa ficar: %v %v", id, found, err)
		}
	}
	var camps []int64
	rows, _ := pool.Query(ctx, `SELECT id FROM campaigns ORDER BY id`)
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		camps = append(camps, id)
	}
	rows.Close()
	if len(camps) != 2 || camps[0] != andamento {
		t.Errorf("campanhas que ficaram: %v (a encerrada velha sai)", camps)
	}
	// De novo: nada mais a apagar. E Jobs = 0 guarda os jobs para sempre.
	mustExec(`UPDATE jobs SET created_at = now() - interval '900 days', finished_at = now() - interval '900 days' WHERE state = 'concluido'`)
	if got, err := st.Prune(ctx, Retention{History: r.History}, time.Now(), false); err != nil || got.Total() != 0 {
		t.Errorf("sem retenção de jobs: %+v %v", got, err)
	}

	sizes, total, err := st.TableSizes(ctx)
	if err != nil || sizes["inventory_reports"] <= 0 || sizes["jobs"] <= 0 || total < sizes["inventory_reports"] {
		t.Errorf("tamanhos: %v %d %v", sizes, total, err)
	}
}
