package bundle

import (
	"archive/zip"
	"errors"
	"io"
	"strings"
)

// PackScript normaliza um script avulso para o mesmo ZIP consumido pelos agentes.
// O nome é preservado; executáveis binários continuam exigindo um pacote ZIP.
func PackScript(w io.Writer, name string, r io.Reader) error {
	if !ValidEntry(name) || strings.Contains(name, "/") || RunnerFor(name) == "" || RunnerFor(name) == RunnerExe {
		return errors.New("script avulso: use .ps1, .bat, .cmd, .vbs ou .sh, com nome simples")
	}
	z := zip.NewWriter(w)
	// Store evita recusar scripts de texto repetitivo como ZIP bomb e produz um pacote
	// determinístico: reenviar o mesmo arquivo resulta no mesmo SHA-256.
	f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxScriptBytes+1))
	if err != nil {
		return err
	}
	if n > MaxScriptBytes {
		return errors.New("script avulso maior que 16 MiB; envie um ZIP")
	}
	return z.Close()
}
