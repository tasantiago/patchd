package main

import "testing"

// Aula 18.2: o nome do painel sem as aspas do .env; comprido demais é recusado (e avisado).
func TestNomeDoPainel(t *testing.T) {
	for v, quer := range map[string]string{
		`patchd TJRO`:                        "patchd TJRO",
		`"patchd TJRO"`:                      "patchd TJRO",
		` 'patchd TJRO' `:                    "patchd TJRO",
		`"patchd`:                            `"patchd`,                            // aspas sem par ficam
		`Tribunal de Justiça · Atualizações`: "Tribunal de Justiça · Atualizações", // 34 letras, mais bytes
	} {
		if got, ok := panelTitle(v); !ok || got != quer {
			t.Errorf("%q: %q %v, quer %q", v, got, ok, quer)
		}
	}
	for _, v := range []string{`""`, "   ", "um nome de painel comprido demais para a barra verde"} {
		if _, ok := panelTitle(v); ok {
			t.Errorf("%q aceito", v)
		}
	}
}
