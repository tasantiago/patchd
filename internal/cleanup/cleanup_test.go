package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

// A varredura: sai só o arquivo velho; o novo (em uso) fica; um link para fora da pasta não é
// seguido (o arquivo do outro lado fica); a subpasta velha que esvaziou sai.
func TestVarredura(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "dados")
	fora := filepath.Join(base, "fora")
	for _, d := range []string{root, fora, filepath.Join(root, "pacotes", "job-7"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	velho := time.Now().Add(-72 * time.Hour)
	escreve := func(p string, n int, quando time.Time) {
		t.Helper()
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, quando, quando); err != nil {
			t.Fatal(err)
		}
	}
	escreve(filepath.Join(root, "pacotes", "job-7", "instalador.msi"), 3000, velho)
	escreve(filepath.Join(root, "pacotes", "novo.msi"), 500, time.Now())
	escreve(filepath.Join(fora, "importante.txt"), 100, velho)
	if err := os.Symlink(fora, filepath.Join(root, "pacotes", "link-pasta")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fora, "importante.txt"), filepath.Join(root, "pacotes", "link-arquivo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "pacotes", "job-7"), velho, velho); err != nil {
		t.Fatal(err)
	}

	c := New(root, nil)
	livre := int64(10 << 30)
	c.free = func() int64 { livre += 3000; return livre }
	rel, err := c.Clean(context.Background(), jobs.CleanupParams{Items: []string{jobs.CleanupPatchd}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fora, "importante.txt")); err != nil {
		t.Fatal("o link levou a limpeza para fora da pasta")
	}
	if _, err := os.Stat(filepath.Join(root, "pacotes", "novo.msi")); err != nil {
		t.Error("apagou o arquivo novo (em uso)")
	}
	if _, err := os.Stat(filepath.Join(root, "pacotes", "job-7")); !os.IsNotExist(err) {
		t.Error("a subpasta velha que esvaziou ficou")
	}
	if !strings.Contains(rel, "restos do patchd: 2 KB (1 arquivo(s), 2 em uso ou pulados)") || !strings.Contains(rel, "limpeza: livre no disco do sistema 10,0 GB") {
		t.Errorf("resumo: %s", rel)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, quer := range map[int64]string{500: "500 B", 2048: "2 KB", 5 << 20: "5 MB", 3 << 29: "1,5 GB"} {
		if got := FormatBytes(n); got != quer {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func TestItensDaLimpeza(t *testing.T) {
	for _, p := range []jobs.CleanupParams{{}, {Items: []string{"C:\\Windows"}}, {Items: []string{jobs.CleanupPatchd, jobs.CleanupPatchd}}} {
		if p.Validate() == nil {
			t.Errorf("deveria recusar %+v", p)
		}
	}
	if (jobs.CleanupParams{Items: jobs.CleanupItems}).Validate() != nil {
		t.Error("todos os itens")
	}
	if _, err := jobs.ParseCleanupParams([]byte(`{"items":["patchd"],"path":"C:\\"}`)); err == nil {
		t.Error("campo desconhecido (caminho) aceito")
	}
}
