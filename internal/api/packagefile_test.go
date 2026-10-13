package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestDownloadDeInstalador(t *testing.T) {
	h, m, _, dir, _ := ambienteConteudo(t)
	inst := []byte("MZ instalador do notepad++")
	soma := sha256.Sum256(inst)
	nome := hex.EncodeToString(soma[:]) + ".exe"
	os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	if err := os.WriteFile(filepath.Join(dir, "pacotes", nome), inst, 0o640); err != nil {
		t.Fatal(err)
	}
	// Com a credencial da máquina: o arquivo, com o SHA-256 como ETag.
	r := envia(h, "GET", protocol.PackagesPath+nome, m.Credential, nil)
	if r.Code != http.StatusOK || r.Body.String() != string(inst) || r.Header().Get("ETag") != `"`+strings.TrimSuffix(nome, ".exe")+`"` {
		t.Fatalf("download: %d %q %q", r.Code, r.Body, r.Header().Get("ETag"))
	}
	// Sem credencial: 401.
	if r := envia(h, "GET", protocol.PackagesPath+nome, "", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", r.Code)
	}
	// Nomes fora do formato e arquivos que não existem: 404, sem tocar no disco fora da pasta.
	for _, n := range []string{"..%2Fwsusscn2.cab", strings.Repeat("a", 64) + ".msi", strings.ToUpper(nome), strings.TrimSuffix(nome, ".exe") + ".bat", "x.exe"} {
		if r := envia(h, "GET", protocol.PackagesPath+n, m.Credential, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", n, r.Code)
		}
	}
}
