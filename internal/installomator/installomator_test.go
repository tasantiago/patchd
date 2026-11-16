package installomator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixada(t *testing.T) {
	nada := func(string) (string, bool) { return "", false }
	if s, err := Pinned(nada); err != nil || s.Version != Version || s.SHA256 != SHA256 {
		t.Fatalf("fixada: %+v %v", s, err)
	}
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	h := strings.Repeat("A", 64)
	if s, err := Pinned(env(map[string]string{"PATCHD_INSTALLOMATOR_VERSION": "10.11", "PATCHD_INSTALLOMATOR_SHA256": h})); err != nil ||
		s.Version != "10.11" || s.SHA256 != strings.ToLower(h) {
		t.Errorf("trocada: %+v %v", s, err)
	}
	for _, m := range []map[string]string{
		{"PATCHD_INSTALLOMATOR_VERSION": "10.11"},
		{"PATCHD_INSTALLOMATOR_SHA256": h},
		{"PATCHD_INSTALLOMATOR_VERSION": "main", "PATCHD_INSTALLOMATOR_SHA256": h},
		{"PATCHD_INSTALLOMATOR_VERSION": "10.11", "PATCHD_INSTALLOMATOR_SHA256": "xyz"},
	} {
		if _, err := Pinned(env(m)); err == nil {
			t.Errorf("%v devia ser recusada", m)
		}
	}
	if u := (Script{Version: "10.10"}).URL(BaseURL + "/"); u != "https://raw.githubusercontent.com/Installomator/Installomator/v10.10/Installomator.sh" {
		t.Errorf("endereço: %s", u)
	}
}

// O script só fica no cache se o SHA-256 for o fixado; o que já está lá é conferido, não
// confiado pelo nome.
func TestBaixar(t *testing.T) {
	conteudo := []byte("#!/bin/zsh\necho Installomator\n")
	soma := sha256.Sum256(conteudo)
	certo := Script{Version: "10.10", SHA256: hex.EncodeToString(soma[:])}
	pedidos := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos++
		if r.URL.Path != "/v10.10/Installomator.sh" || r.Header.Get("User-Agent") != "patchd-teste" {
			http.NotFound(w, r)
			return
		}
		w.Write(conteudo)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ctx := context.Background()
	n, cached, err := Fetch(ctx, srv.Client(), "patchd-teste", srv.URL, dir, certo)
	if err != nil || cached || n != int64(len(conteudo)) {
		t.Fatalf("primeira: %d %v %v", n, cached, err)
	}
	if _, cached, err = Fetch(ctx, srv.Client(), "patchd-teste", srv.URL, dir, certo); err != nil || !cached || pedidos != 1 {
		t.Errorf("segunda (do cache): %v %v, %d pedidos", cached, err, pedidos)
	}
	if n, err := Check(dir, certo); err != nil || n != int64(len(conteudo)) {
		t.Errorf("conferência: %d %v", n, err)
	}
	// Alguém mexeu no arquivo do cache: a conferência recusa, e o Fetch baixa de novo.
	path := filepath.Join(dir, FileName(certo.SHA256))
	if err := os.WriteFile(path, []byte("rm -rf /"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir, certo); err == nil {
		t.Error("arquivo adulterado passou na conferência")
	}
	if _, cached, err = Fetch(ctx, srv.Client(), "patchd-teste", srv.URL, dir, certo); err != nil || cached || pedidos != 2 {
		t.Errorf("depois de adulterado: %v %v, %d pedidos", cached, err, pedidos)
	}
	// Outro SHA-256 fixado: o que vem não confere e nada é guardado.
	errado := Script{Version: "10.10", SHA256: strings.Repeat("0", 64)}
	if _, _, err := Fetch(ctx, srv.Client(), "patchd-teste", srv.URL, dir, errado); err == nil || !strings.Contains(err.Error(), "nada foi guardado") {
		t.Errorf("hash diferente: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName(errado.SHA256))); err == nil {
		t.Error("guardou o que não confere")
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("sobrou temporário: %v", es)
	}
	if _, _, err := Fetch(ctx, srv.Client(), "patchd-teste", srv.URL, dir, Script{Version: "9.9", SHA256: certo.SHA256 + ""}); err != nil {
		// a versão 9.9 não existe no servidor de teste, mas o arquivo com o mesmo hash já está no cache
		t.Errorf("mesmo hash, do cache: %v", err)
	}
}
