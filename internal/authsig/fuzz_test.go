package authsig

import (
	"bytes"
	"testing"

	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
)

// Arquivo qualquer (enviado pelo painel, ou o que um servidor do fabricante entregar): erro,
// nunca pânico nem laço.
func FuzzRead(f *testing.F) {
	f.Add(authsigtest.Exe("Org"))
	f.Add(authsigtest.Exe(""))
	f.Add(append(append([]byte{}, cfbMagic...), make([]byte, 2048)...))
	f.Add([]byte("MZ"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Read(bytes.NewReader(b), int64(len(b)))
	})
}
