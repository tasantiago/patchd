package store

import (
	"context"
	"strings"
	"testing"
)

// TestColacaoDasChavesEstrangeiras impede a volta do problema da Aula 7.8: uma chave
// estrangeira com colação diferente da chave referenciada faz o PostgreSQL ignorar o
// índice nas junções e ler a tabela inteira.
func TestColacaoDasChavesEstrangeiras(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `
		SELECT c.conrelid::regclass::text || '.' || a.attname || ' -> ' || c.confrelid::regclass::text || '.' || af.attname
		FROM pg_constraint c
		JOIN LATERAL unnest(c.conkey, c.confkey) AS k(col, refcol) ON true
		JOIN pg_attribute a  ON a.attrelid = c.conrelid  AND a.attnum = k.col
		JOIN pg_attribute af ON af.attrelid = c.confrelid AND af.attnum = k.refcol
		WHERE c.contype = 'f' AND a.attcollation <> af.attcollation
		  AND c.connamespace = current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ruins []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		ruins = append(ruins, s)
	}
	if len(ruins) > 0 {
		t.Errorf("chaves estrangeiras com colação diferente da referenciada (o índice não serve às junções):\n%s", strings.Join(ruins, "\n"))
	}
}
