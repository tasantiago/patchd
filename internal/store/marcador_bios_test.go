package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// A migration 0070 tira do banco a senha da BIOS que o inventário trouxe como versão do
// marcador do .bat, sem tocar nos outros programas, e retém as respostas que a citavam.
func TestMigration0070TiraASenhaDoMarcador(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	p := NewPostgres(pool)
	const senha = "Exemplo@12345678"
	const uninstall = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	now := time.Now().UTC()
	suja := protocol.InventoryReport{Hash: "h1", CollectedAt: now, Software: []protocol.Software{
		{Name: "7-Zip", Version: "24.08", Scope: "machine", Source: uninstall + `\7-Zip`},
		{Name: "BIOS_password", Version: senha, Scope: "machine", SystemComponent: true, Source: uninstall + `\BIOS_password`},
		{Name: "Outro nome", Version: senha, Scope: "machine", Source: uninstall + `\bios_password`},
	}}
	limpa := protocol.InventoryReport{Hash: "h2", CollectedAt: now, Software: []protocol.Software{
		{Name: "Gerenciador de Password", Version: "2.1", Scope: "machine", Source: uninstall + `\{X}`},
	}}
	semLista := protocol.InventoryReport{Hash: "h3", CollectedAt: now} // software: null
	for id, rep := range map[string]protocol.InventoryReport{"m-suja": suja, "m-limpa": limpa, "m-sem": semLista} {
		if _, err := p.SaveInventory(ctx, id, rep); err != nil {
			t.Fatal(err)
		}
	}
	insert := `INSERT INTO jobs (machine_id, type, state, payload, signature, created_by, not_after, detail)
		VALUES ('m-suja', $1, 'concluido', '{}', 'x', 'teste', now(), $2)`
	if _, err := pool.Exec(ctx, insert, "pergunta", `{"value":"`+senha+`","extra":"BIOS_password `+senha+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, "pergunta", `{"value":"24.08","extra":"7-Zip 24.08"}`); err != nil {
		t.Fatal(err)
	}

	sql, err := migrationFiles.ReadFile("migrations/0070_marcador_senha_bios.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // de novo: não muda mais nada
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}

	var sobrou int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM inventory_reports WHERE position($1 in report::text) > 0) +
		(SELECT count(*) FROM jobs WHERE position($1 in detail) > 0)`, senha).Scan(&sobrou); err != nil {
		t.Fatal(err)
	}
	if sobrou != 0 {
		t.Errorf("a senha continua em %d linha(s)", sobrou)
	}
	rep, _, err := p.LatestInventory(ctx, "m-suja")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Software) != 3 || rep.Software[0].Name != "7-Zip" || rep.Software[0].Version != "24.08" ||
		rep.Software[1].Name != "BIOS_password" || rep.Software[1].Version != "" || !rep.Software[1].SystemComponent || rep.Software[2].Version != "" {
		t.Errorf("lista depois da limpeza: %+v", rep.Software)
	}
	if rep, _, _ := p.LatestInventory(ctx, "m-limpa"); len(rep.Software) != 1 || rep.Software[0].Version != "2.1" {
		t.Errorf("o programa de verdade mudou: %+v", rep.Software)
	}
	if rep, found, err := p.LatestInventory(ctx, "m-sem"); err != nil || !found || rep.Software != nil {
		t.Errorf("relatório sem lista: %+v, %v, %v", rep.Software, found, err)
	}
	var retidas, intactas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE detail LIKE '%resposta retida%'), count(*) FILTER (WHERE detail LIKE '%7-Zip 24.08%') FROM jobs`).
		Scan(&retidas, &intactas); err != nil {
		t.Fatal(err)
	}
	if retidas != 1 || intactas != 1 {
		t.Errorf("respostas: %d retida(s) e %d intacta(s), queria 1 e 1", retidas, intactas)
	}
}
