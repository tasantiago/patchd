package version

import "testing"

func TestCompareSemver(t *testing.T) {
	casos := []struct {
		a, b string
		quer int
	}{
		{"v0.5.0", "v0.5.0", 0},
		{"v0.5.1", "v0.5.0", 1},
		{"v0.10.0", "v0.9.9", 1},     // número, não texto
		{"v0.5.0-dev", "v0.5.0", -1}, // pré-lançamento antes do lançamento
		{"v0.5.1-dev", "v0.5.0", 1},
		{"v0.5.0-rc.2", "v0.5.0-rc.10", -1},
		{"v0.5.0-rc.1", "v0.5.0-rc.a", -1}, // numérico antes de texto
		{"v0.5.0-rc", "v0.5.0-rc.1", -1},
		{"v1.0.0+build.7", "v1.0.0", 0}, // build ignorado
	}
	for _, c := range casos {
		got, err := CompareSemver(c.a, c.b)
		if err != nil || got != c.quer {
			t.Errorf("CompareSemver(%q, %q) = %d, %v; quer %d", c.a, c.b, got, err, c.quer)
		}
	}
	for _, ruim := range []string{"0.5.0", "dev", "v0.5", "v0.5.x", "v01.5.0", "v0.5.0-", "v0.5.0-a..b", "(devel)"} {
		if _, err := CompareSemver(ruim, "v0.5.0"); err == nil {
			t.Errorf("%q deveria ser recusada", ruim)
		}
	}
}
