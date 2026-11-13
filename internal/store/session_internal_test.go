package store

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// As conexões do patchd desligam o JIT do PostgreSQL (a compilação custava centenas de
// milissegundos nas listas do parque), a menos que a URL do banco diga outra coisa.
func TestPadroesDaSessaoDoBanco(t *testing.T) {
	for url, quer := range map[string]string{
		"postgres://u@127.0.0.1:5432/patchd":                         "off",
		"postgres://u@127.0.0.1:5432/patchd?sslmode=disable":         "off",
		"postgres://u@127.0.0.1:5432/patchd?jit=on":                  "on",
		"postgres://u@127.0.0.1:5432/patchd?jit=off&sslmode=disable": "off",
	} {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		sessionDefaults(cfg)
		if got := cfg.ConnConfig.RuntimeParams["jit"]; got != quer {
			t.Errorf("%s: jit=%q, esperava %q", url, got, quer)
		}
	}
}
