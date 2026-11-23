package choco

import "testing"

// Script qualquer (o pacote é de terceiros): erro ou resultado, nunca pânico.
func FuzzExtract(f *testing.F) {
	f.Add(scriptChrome, "", "")
	f.Add(scriptAdobe, "$MUIurl64", "$MUIchecksum64")
	f.Fuzz(func(t *testing.T, script, u, c string) {
		_, _ = Extract(script, u, c)
	})
}

// Pacote qualquer.
func FuzzRead(f *testing.F) {
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Read(b) })
}
