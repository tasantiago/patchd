package patch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// catalogInfo calcula o SHA-256 (em fluxo, sem carregar o arquivo na memória) e a data
// de modificação do catálogo. O servidor, que distribui o arquivo, sabe a qual
// publicação da Microsoft cada hash corresponde.
func catalogInfo(path string) (string, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("catálogo offline: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("catálogo offline: %w", err)
	}
	if st.IsDir() {
		return "", time.Time{}, fmt.Errorf("catálogo offline: %s é uma pasta", path)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", time.Time{}, fmt.Errorf("catálogo offline: ler %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), st.ModTime().UTC(), nil
}
