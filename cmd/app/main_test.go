package main

import (
	"slices"
	"testing"
)

// Sem argumento (o atalho, o Explorer): abre a página; "app" (o logon): só o ícone.
func TestArgumentos(t *testing.T) {
	for _, c := range []struct{ in, quer []string }{
		{nil, []string{"-abrir"}},
		{[]string{"app"}, []string{}},
		{[]string{"app", "-no-tray"}, []string{"-no-tray"}},
		{[]string{"-no-tray"}, []string{"-no-tray"}},
	} {
		if got := appArgs(c.in); !slices.Equal(got, c.quer) {
			t.Errorf("%q: %q, quer %q", c.in, got, c.quer)
		}
	}
}
