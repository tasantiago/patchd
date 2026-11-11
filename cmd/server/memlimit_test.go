package main

import "testing"

func TestLimiteBrandoDeMemoria(t *testing.T) {
	const gb = 1 << 30
	casos := []struct {
		nome   string
		env    string
		set    bool
		budget uint64
		cg     bool
		quer   int64
		ok     bool
	}{
		{"máquina de 3,6 GB, sem limite no container", "", false, 3865470566, false, 1546188226, true},
		{"container com limite de 2 GB", "", false, 2 * gb, true, 1610612736, true},
		{"máquina pequena: o piso", "", false, gb, false, 512 << 20, true},
		{"GOMEMLIMIT no ambiente vale", "1GiB", true, 8 * gb, false, 0, false},
		{"GOMEMLIMIT=off também", "off", true, 8 * gb, false, 0, false},
		{"memória desconhecida", "", false, 0, false, 0, false},
	}
	for _, c := range casos {
		got, ok := softMemoryLimit(c.env, c.set, c.budget, c.cg)
		if got != c.quer || ok != c.ok {
			t.Errorf("%s: %d %v (queria %d %v)", c.nome, got, ok, c.quer, c.ok)
		}
	}
}
