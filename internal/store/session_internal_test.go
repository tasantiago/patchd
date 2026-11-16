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

// O pool: 4 conexões por núcleo, de 8 a 16, a menos que a URL diga o número.
func TestPoolPadrao(t *testing.T) {
	for _, c := range []struct {
		url  string
		cpus int
		quer int32
	}{
		{"postgres://u@h/patchd", 1, 8},
		{"postgres://u@h/patchd", 2, 8},
		{"postgres://u@h/patchd", 3, 12},
		{"postgres://u@h/patchd", 6, 16},
		{"postgres://u@h/patchd?pool_max_conns=4", 6, 4},
		{"postgres://u@h/patchd?sslmode=disable&pool_max_conns=30", 2, 30},
	} {
		cfg, err := pgxpool.ParseConfig(c.url)
		if err != nil {
			t.Fatal(err)
		}
		sessionDefaultsFor(cfg, c.cpus, c.url)
		if cfg.MaxConns != c.quer {
			t.Errorf("%s com %d núcleos: %d, esperava %d", c.url, c.cpus, cfg.MaxConns, c.quer)
		}
	}
}
