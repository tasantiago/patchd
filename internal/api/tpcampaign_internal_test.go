package api

import "testing"

func TestNameVersion(t *testing.T) {
	for _, c := range [][3]string{
		{"LibreOffice 26.8.1.1", "26.8.1.1", "LibreOffice 26.8.1.1"},
		{"Notepad++ (64-bit x64)", "8.9.8.1", "Notepad++ (64-bit x64) 8.9.8.1"},
		{"Programa", "", "Programa"},
	} {
		if got := nameVersion(c[0], c[1]); got != c[2] {
			t.Errorf("%q %q: %q", c[0], c[1], got)
		}
	}
}
